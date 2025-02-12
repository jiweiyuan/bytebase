package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bytebase/bytebase/api"
	"github.com/bytebase/bytebase/common"
	"github.com/bytebase/bytebase/external/gitlab"
	"github.com/bytebase/bytebase/plugin/db"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

var (
	gitLabWebhookPath = "hook/gitlab"
)

func (s *Server) registerWebhookRoutes(g *echo.Group) {
	g.POST("/gitlab/:id", func(c echo.Context) error {
		ctx := context.Background()
		var b []byte
		b, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Failed to read webhook request").SetInternal(err)
		}

		pushEvent := &gitlab.WebhookPushEvent{}
		if err := json.Unmarshal(b, pushEvent); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Malformatted push event").SetInternal(err)
		}

		// This shouldn't happen as we only setup webhook to receive push event, just in case.
		if pushEvent.ObjectKind != gitlab.WebhookPush {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid webhook event type, got %s, want push", pushEvent.ObjectKind))
		}

		webhookEndpointId := c.Param("id")
		repositoryFind := &api.RepositoryFind{
			WebhookEndpointId: &webhookEndpointId,
		}
		repository, err := s.RepositoryService.FindRepository(ctx, repositoryFind)
		if err != nil {
			if common.ErrorCode(err) == common.NotFound {
				return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("Endpoint not found: %v", webhookEndpointId))
			}
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to respond webhook event for endpoint: %v", webhookEndpointId)).SetInternal(err)
		}

		if err := s.ComposeRepositoryRelationship(ctx, repository); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to fetch repository relationship: %v", repository.Name)).SetInternal(err)
		}

		if c.Request().Header.Get("X-Gitlab-Token") != repository.WebhookSecretToken {
			return echo.NewHTTPError(http.StatusBadRequest, "Secret token mismatch")
		}

		if strconv.Itoa(pushEvent.Project.ID) != repository.ExternalId {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Project mismatch, got %d, want %s", pushEvent.Project.ID, repository.ExternalId))
		}

		createdMessageList := []string{}
		for _, commit := range pushEvent.CommitList {
			for _, added := range commit.AddedList {
				if !strings.HasPrefix(added, repository.BaseDirectory) {
					s.l.Debug("Ignored committed file, not under base directory.", zap.String("file", added), zap.String("base_directory", repository.BaseDirectory))
					continue
				}

				createdTime, err := time.Parse(time.RFC3339, commit.Timestamp)
				if err != nil {
					s.l.Warn("Ignored committed file, failed to parse commit timestamp.", zap.String("file", added), zap.String("timestamp", commit.Timestamp), zap.Error(err))
				}

				// Ignored the schema file we auto generated to the repository.
				if repository.SchemaPathTemplate != "" {
					placeholderList := []string{
						"ENV_NAME",
						"DB_NAME",
					}
					schemafilePathRegex := repository.SchemaPathTemplate
					for _, placeholder := range placeholderList {
						schemafilePathRegex = strings.ReplaceAll(schemafilePathRegex, fmt.Sprintf("{{%s}}", placeholder), fmt.Sprintf("(?P<%s>[a-zA-Z0-9+-=/_#?!$. ]+)", placeholder))
					}
					myRegex, err := regexp.Compile(schemafilePathRegex)
					if err != nil {
						s.l.Warn("Invalid schema path template.", zap.String("schema_path_template",
							repository.SchemaPathTemplate),
							zap.Error(err),
						)
					}
					if myRegex.MatchString(added) {
						continue
					}
				}

				vcsPushEvent := common.VCSPushEvent{
					VCSType:            repository.VCS.Type,
					BaseDirectory:      repository.BaseDirectory,
					Ref:                pushEvent.Ref,
					RepositoryID:       strconv.Itoa(pushEvent.Project.ID),
					RepositoryURL:      pushEvent.Project.WebURL,
					RepositoryFullPath: pushEvent.Project.FullPath,
					AuthorName:         pushEvent.AuthorName,
					FileCommit: common.VCSFileCommit{
						ID:         commit.ID,
						Title:      commit.Title,
						Message:    commit.Message,
						CreatedTs:  createdTime.Unix(),
						URL:        commit.URL,
						AuthorName: commit.Author.Name,
						Added:      added,
					},
				}

				// Create a WARNING project activity if committed file is ignored
				var createIgnoredFileActivity = func(err error) {
					s.l.Warn("Ignored committed file", zap.String("file", added), zap.Error(err))
					bytes, marshalErr := json.Marshal(api.ActivityProjectRepositoryPushPayload{
						VCSPushEvent: vcsPushEvent,
					})
					if marshalErr != nil {
						s.l.Warn("Failed to construct project activity payload to record ignored repository committed file", zap.Error(marshalErr))
						return
					}

					activityCreate := &api.ActivityCreate{
						CreatorId:   api.SYSTEM_BOT_ID,
						ContainerId: repository.ProjectId,
						Type:        api.ActivityProjectRepositoryPush,
						Level:       api.ACTIVITY_WARN,
						Comment:     fmt.Sprintf("Ignored committed file %q, %s.", added, err.Error()),
						Payload:     string(bytes),
					}
					_, err = s.ActivityManager.CreateActivity(ctx, activityCreate, &ActivityMeta{})
					if err != nil {
						s.l.Warn("Failed to create project activity to record ignored repository committed file", zap.Error(err))
					}
				}

				mi, err := db.ParseMigrationInfo(added, filepath.Join(repository.BaseDirectory, repository.FilePathTemplate))
				if err != nil {
					createIgnoredFileActivity(err)
					continue
				}

				// Retrieve sql by reading the file content
				resp, err := gitlab.GET(
					repository.VCS.InstanceURL,
					fmt.Sprintf("projects/%s/repository/files/%s/raw?ref=%s", repository.ExternalId, url.QueryEscape(added), commit.ID),
					repository.AccessToken,
				)
				if err != nil {
					createIgnoredFileActivity(fmt.Errorf("failed to read file: %w", err))
					continue
				}

				b, err := io.ReadAll(resp.Body)
				if err != nil {
					createIgnoredFileActivity(fmt.Errorf("failed to read file response: %w", err))
					continue
				}
				defer resp.Body.Close()

				// Find matching database list
				databaseFind := &api.DatabaseFind{
					ProjectId: &repository.ProjectId,
					Name:      &mi.Database,
				}
				databaseList, err := s.ComposeDatabaseListByFind(ctx, databaseFind)
				if err != nil {
					createIgnoredFileActivity(fmt.Errorf("failed to find database matching database %q referenced by the committed file", mi.Database))
					continue
				} else if len(databaseList) == 0 {
					createIgnoredFileActivity(fmt.Errorf("project ID %d does not own database %q referenced by the committed file", repository.ProjectId, mi.Database))
					continue
				}

				// We support 3 patterns on how to organize the schema files.
				// Pattern 1: 	The database name is the same across all environments. Each environment will have its own directory, so the
				//              schema file looks like "dev/v1__db1", "staging/v1__db1".
				//
				// Pattern 2: 	Like 1, the database name is the same across all environments. All environment shares the same schema file,
				//              say v1__db1, when a new file is added like v2__db1__add_column, we will create a multi stage pipeline where
				//              each stage corresponds to an environment.
				//
				// Pattern 3:  	The database name is different among different environments. In such case, the database name alone is enough
				//             	to identify ambiguity.

				// Further filter by environment name if applicable.
				filterdDatabaseList := []*api.Database{}
				if mi.Environment != "" {
					for _, database := range databaseList {
						// Environment name comparision is case insensitive
						if strings.EqualFold(database.Instance.Environment.Name, mi.Environment) {
							filterdDatabaseList = append(filterdDatabaseList, database)
						}
					}
					if len(filterdDatabaseList) == 0 {
						createIgnoredFileActivity(fmt.Errorf("project does not contain committed file database %q for environment %q", mi.Database, mi.Environment))
						continue
					}
				} else {
					filterdDatabaseList = databaseList
				}

				var pipelineApprovalByEnv = map[int]api.PipelineApprovalValue{}
				{
					// It could happen that for a particular environment a project contain 2 database with the same name.
					// We will emit warning in this case.
					var databaseListByEnv = map[int][]*api.Database{}
					for _, database := range filterdDatabaseList {
						list, ok := databaseListByEnv[database.Instance.EnvironmentId]
						if ok {
							databaseListByEnv[database.Instance.EnvironmentId] = append(list, database)
						} else {
							list := make([]*api.Database, 0)
							databaseListByEnv[database.Instance.EnvironmentId] = append(list, database)
						}

						// Load pipeline approval policy per environment.
						if _, ok := pipelineApprovalByEnv[database.Instance.EnvironmentId]; !ok {
							p, err := s.PolicyService.GetPipelineApprovalPolicy(ctx, database.Instance.EnvironmentId)
							if err != nil {
								createIgnoredFileActivity(fmt.Errorf("failed to find pipeline approval policy for environment %v", database.Instance.EnvironmentId))
								continue
							}
							pipelineApprovalByEnv[database.Instance.EnvironmentId] = p.Value
						}
					}

					var multipleDatabaseForSameEnv = false
					for environemntId, databaseList := range databaseListByEnv {
						if len(databaseList) > 1 {
							multipleDatabaseForSameEnv = true

							s.l.Warn(fmt.Sprintf("Ignored committed file, multiple ambiguous databases named %q for environment %d.", mi.Database, environemntId),
								zap.Int("project_id", repository.ProjectId),
								zap.String("file", added),
							)
						}
					}

					if multipleDatabaseForSameEnv {
						continue
					}
				}

				// Compose the new issue
				stageList := []api.StageCreate{}
				for _, database := range filterdDatabaseList {
					databaseID := database.ID
					taskStatus := api.TaskPendingApproval
					if pipelineApprovalByEnv[database.Instance.Environment.ID] == api.PipelineApprovalValueManualNever {
						taskStatus = api.TaskPending
					}
					task := &api.TaskCreate{
						InstanceId:    database.InstanceId,
						DatabaseId:    &databaseID,
						Name:          mi.Description,
						Status:        taskStatus,
						Type:          api.TaskDatabaseSchemaUpdate,
						Statement:     string(b),
						VCSPushEvent:  &vcsPushEvent,
						MigrationType: mi.Type,
					}
					stageList = append(stageList, api.StageCreate{
						EnvironmentId: database.Instance.EnvironmentId,
						TaskList:      []api.TaskCreate{*task},
						Name:          database.Instance.Environment.Name,
					})
				}
				pipeline := &api.PipelineCreate{
					StageList: stageList,
					Name:      fmt.Sprintf("Pipeline - %s", commit.Title),
				}
				issueCreate := &api.IssueCreate{
					ProjectId:   repository.ProjectId,
					Pipeline:    *pipeline,
					Name:        commit.Title,
					Type:        api.IssueDatabaseSchemaUpdate,
					Description: commit.Message,
					AssigneeId:  api.SYSTEM_BOT_ID,
				}

				issue, err := s.CreateIssue(ctx, issueCreate, api.SYSTEM_BOT_ID)
				if err != nil {
					s.l.Warn("Failed to create update schema task for added repository file", zap.Error(err),
						zap.String("file", added))
					continue
				}

				createdMessageList = append(createdMessageList, fmt.Sprintf("Created issue %q on adding %s", issue.Name, added))

				// Create a project activity after sucessfully creating the issue as the result of the push event
				{
					bytes, err := json.Marshal(api.ActivityProjectRepositoryPushPayload{
						VCSPushEvent: vcsPushEvent,
						IssueId:      issue.ID,
						IssueName:    issue.Name,
					})
					if err != nil {
						return echo.NewHTTPError(http.StatusInternalServerError, "Failed to construct activity payload").SetInternal(err)
					}

					activityCreate := &api.ActivityCreate{
						CreatorId:   api.SYSTEM_BOT_ID,
						ContainerId: repository.ProjectId,
						Type:        api.ActivityProjectRepositoryPush,
						Level:       api.ACTIVITY_INFO,
						Comment:     fmt.Sprintf("Created issue %q.", issue.Name),
						Payload:     string(bytes),
					}
					_, err = s.ActivityManager.CreateActivity(ctx, activityCreate, &ActivityMeta{})
					if err != nil {
						return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to create project activity after creating issue from repository push event: %d", issue.ID)).SetInternal(err)
					}
				}
			}
		}

		return c.String(http.StatusOK, strings.Join(createdMessageList, "\n"))
	})
}
