export { FactoryApp } from "./factory-app.js";
export type { FactoryAppProps } from "./factory-app.js";
export type { FactoryAppStatus } from "./factory-app-controller.js";
export { FactoryConsole } from "./factory-console.js";
export type { FactoryConsoleProps } from "./factory-console.js";
export { AgentStrip, HomeScreen, QueueScreen, StageMeter } from "./console-screens.js";
export {
  STAGE_SEQUENCE,
  agentActivity,
  agentCurrentTask,
  agentGlyph,
  factoryCounters,
  orderTasksForHome,
  stageMeterFill,
  stageOfTask,
} from "./console-view.js";
export type {
  AgentActivity,
  ConsoleScreen,
  FactoryCounters,
  TaskStage,
} from "./console-view.js";
