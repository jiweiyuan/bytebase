<template>
  <div class="flex items-center space-x-4">
    <button
      v-if="showRunCheckButton"
      type="button"
      class="btn-small py-0.5"
      :disabled="hasRunningTaskCheck"
      @click.prevent="runChecks"
    >
      {{ hasRunningTaskCheck ? "Checking..." : "Run checks" }}
    </button>
    <TaskCheckBadgeBar
      :taskCheckRunList="task.taskCheckRunList"
      @select-task-check-run="
        (checkRun) => {
          viewCheckRunDetail(checkRun);
        }
      "
    />
    <BBModal
      v-if="state.showModal"
      :title="`Check result for '${task.name}'`"
      @close="dimissModal"
    >
      <div class="space-y-4 w-208">
        <div>
          <TaskCheckBadgeBar
            :taskCheckRunList="task.taskCheckRunList"
            :allowSelection="true"
            :stickySelection="true"
            :selectedTaskCheckRun="state.selectedTaskCheckRun"
            @select-task-check-run="
              (checkRun) => {
                viewCheckRunDetail(checkRun);
              }
            "
          />
        </div>
        <BBTabFilter
          class="pt-4"
          :tabItemList="tabItemList"
          :selectedIndex="state.selectedTabIndex"
          @select-index="
            (index) => {
              state.selectedTaskCheckRun = tabTaskCheckRunList[index];
              state.selectedTabIndex = index;
            }
          "
        />
        <TaskCheckRunPanel :taskCheckRun="state.selectedTaskCheckRun" />
        <div class="pt-4 flex justify-end">
          <button
            type="button"
            class="btn-primary py-2 px-4"
            @click.prevent="dimissModal"
          >
            Close
          </button>
        </div>
      </div>
    </BBModal>
  </div>
</template>

<script lang="ts">
import { computed, PropType, reactive } from "vue";
import { Task, TaskCheckRun, TaskCheckStatus } from "../types";
import TaskCheckBadgeBar from "./TaskCheckBadgeBar.vue";
import TaskCheckRunPanel from "./TaskCheckRunPanel.vue";
import { BBTabFilterItem } from "../bbkit/types";
import { cloneDeep } from "lodash";
import { humanizeTs } from "../utils";

interface LocalState {
  showModal: boolean;
  selectedTaskCheckRun?: TaskCheckRun;
  selectedTabIndex: number;
}

export default {
  name: "TaskCheckBar",
  emits: ["run-checks"],
  props: {
    task: {
      required: true,
      type: Object as PropType<Task>,
    },
  },
  components: { TaskCheckBadgeBar, TaskCheckRunPanel },
  setup(props, { emit }) {
    const state = reactive<LocalState>({
      showModal: false,
      selectedTabIndex: 0,
    });

    const tabTaskCheckRunList = computed((): TaskCheckRun[] => {
      if (!state.selectedTaskCheckRun) {
        return [];
      }

      const list: TaskCheckRun[] = [];
      for (const check of props.task.taskCheckRunList) {
        if (check.type == state.selectedTaskCheckRun.type) {
          list.push(check);
        }
      }
      const clonedList = cloneDeep(list);
      clonedList.sort(
        (a: TaskCheckRun, b: TaskCheckRun) => b.createdTs - a.createdTs
      );
      return clonedList;
    });

    const tabItemList = computed((): BBTabFilterItem[] => {
      return tabTaskCheckRunList.value.map((item, index) => {
        return {
          title: index == 0 ? "Latest" : humanizeTs(item.createdTs),
          alert: false,
        };
      });
    });

    const showRunCheckButton = computed((): boolean => {
      return (
        props.task.type == "bb.task.database.schema.update" &&
        (props.task.status == "PENDING" ||
          props.task.status == "PENDING_APPROVAL" ||
          props.task.status == "RUNNING" ||
          props.task.status == "FAILED")
      );
    });

    const hasRunningTaskCheck = computed((): boolean => {
      for (const check of props.task.taskCheckRunList) {
        if (check.status == "RUNNING") {
          return true;
        }
      }
      return false;
    });

    // Returns the most severe status
    const taskCheckStatus = (taskCheckRun: TaskCheckRun): TaskCheckStatus => {
      let value: TaskCheckStatus = "SUCCESS";
      for (const result of taskCheckRun.result.resultList) {
        if (result.status == "ERROR") {
          return "ERROR";
        }
        if (result.status == "WARN") {
          value = "WARN";
        }
      }
      return value;
    };

    const viewCheckRunDetail = (taskCheckRun: TaskCheckRun) => {
      state.selectedTaskCheckRun = taskCheckRun;
      state.showModal = true;
    };

    const dimissModal = () => {
      state.showModal = false;
      state.selectedTaskCheckRun = undefined;
    };

    const runChecks = () => {
      emit("run-checks", props.task);
    };

    return {
      state,
      tabTaskCheckRunList,
      tabItemList,
      showRunCheckButton,
      hasRunningTaskCheck,
      taskCheckStatus,
      viewCheckRunDetail,
      dimissModal,
      runChecks,
    };
  },
};
</script>
