export { FactoryApp } from "./factory-app.js";
export type { FactoryAppProps } from "./factory-app.js";
export type { FactoryAppStatus } from "./factory-app-controller.js";
export { FactoryConsole } from "./factory-console.js";
export type { FactoryConsoleProps } from "./factory-console.js";
export { AgentStrip, HomeScreen, QueueScreen, TaskScreen, Ticker, StageMeter } from "./console-screens.js";
export {
  STAGE_SEQUENCE,
  agentActivity,
  agentCurrentTask,
  agentGlyph,
  factoryCounters,
  orderTasksForHome,
  stageMeterFill,
  stageOfTask,
  unavailableQueueActions,
} from "./console-view.js";
export type {
  AgentActivity,
  ConsoleActionUnavailable,
  ConsoleExtras,
  ConsoleScreen,
  FactoryCounters,
  QueueActions,
  SuggestionItem,
  SuggestionOrigin,
  TaskDiffStat,
  TaskRecord,
  TaskRecordFile,
  TaskReview,
  TaskStage,
  TickerEntry,
} from "./console-view.js";
