<template>
  <BBTable
    :columnList="COLUMN_LIST"
    :dataSource="instanceUserList"
    :showHeader="true"
    :rowClickable="false"
    :leftBordered="true"
    :rightBordered="true"
  >
    <template v-slot:body="{ rowData: instanceUser }">
      <BBTableCell :leftPadding="4" class="w-4">
        {{ instanceUser.name }}
      </BBTableCell>
      <BBTableCell class="whitespace-pre-wrap">
        {{ instanceUser.grant.replaceAll("\n", "\n\n") }}
      </BBTableCell>
    </template>
  </BBTable>
</template>

<script lang="ts">
import { PropType } from "vue";
import { BBTableColumn } from "../bbkit/types";
import { InstanceUser } from "../types/InstanceUser";

const COLUMN_LIST: BBTableColumn[] = [
  {
    title: "User",
  },
  {
    title: "Grants",
  },
];

export default {
  name: "InstanceUserTable",
  components: {},
  props: {
    instanceUserList: {
      required: true,
      type: Object as PropType<InstanceUser[]>,
    },
  },
  setup(props, ctx) {
    return {
      COLUMN_LIST,
    };
  },
};
</script>
