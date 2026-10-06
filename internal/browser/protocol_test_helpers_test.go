package browser

import (
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	_ "unsafe"
)

//go:linkname testProtocolEncode github.com/dark-factory-build/dark-factory/internal/browserprotocol.encodeControl
func testProtocolEncode(browserprotocol.MessageType, string, any) ([]byte, error)

//go:linkname testProtocolDecode github.com/dark-factory-build/dark-factory/internal/browserprotocol.decodeControl
func testProtocolDecode([]byte, byte) (browserprotocol.ControlFrame, error)

//go:linkname testProtocolEncodeTerminal github.com/dark-factory-build/dark-factory/internal/browserprotocol.encodeTerminalFrame
func testProtocolEncodeTerminal(browserprotocol.TerminalFrame) ([]byte, error)

func testEncodePairProve(id string, v browserprotocol.PairProve) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypePairProve, id, v)
}
func testEncodeAuthProve(id string, v browserprotocol.AuthProve) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeAuthProve, id, v)
}
func testEncodeStateGet(id string, v browserprotocol.StateGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeStateGet, id, v)
}
func testEncodeStateWatch(id string, v browserprotocol.StateWatch) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeStateWatch, id, v)
}
func testEncodeHumanRequestDetailGet(id string, v browserprotocol.HumanRequestDetailGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeHumanRequestDetailGet, id, v)
}
func testEncodeTaskListGet(id string, v browserprotocol.TaskListGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTaskListGet, id, v)
}
func testEncodeAgentControl(id string, v browserprotocol.AgentControl) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeAgentControl, id, v)
}
func testEncodeTaskHistoryGet(id string, v browserprotocol.TaskHistoryGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTaskHistoryGet, id, v)
}
func testEncodeTaskDetailGet(id string, v browserprotocol.TaskDetailGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTaskDetailGet, id, v)
}
func testEncodeTaskAttachment(id string, v browserprotocol.TaskAttachmentChunk) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTaskAttachment, id, v)
}
func testEncodeTaskEnqueue(id string, v browserprotocol.TaskEnqueue) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTaskEnqueue, id, v)
}
func testEncodeProjectContent(id string, v browserprotocol.ProjectContent) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeProjectContent, id, v)
}
func testEncodeHumanRequestReply(id string, v browserprotocol.HumanRequestReply) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeHumanRequestReply, id, v)
}
func testEncodeHumanRequestCancelRun(id string, v browserprotocol.HumanRequestCancelRun) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeHumanRequestCancelRun, id, v)
}
func testEncodeTerminalAttach(id string, v browserprotocol.TerminalAttach) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTerminalAttach, id, v)
}
func testEncodeTerminalTargetGet(id string, v browserprotocol.TerminalTargetGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTerminalTargetGet, id, v)
}
func testEncodeTerminalAck(v browserprotocol.TerminalAck) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTerminalAck, "", v)
}
func testEncodeTerminalLeaseAcquire(id string, v browserprotocol.TerminalLeaseAcquire) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTerminalLeaseAcquire, id, v)
}
func testEncodeTerminalLeaseRenew(id string, v browserprotocol.TerminalLeaseRenew) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTerminalLeaseRenew, id, v)
}
func testEncodeTerminalDetach(id string, v browserprotocol.TerminalDetach) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeTerminalDetach, id, v)
}
func testEncodeRemoteInvite(id string, v browserprotocol.RemoteInvite) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeRemoteInvite, id, v)
}
func testEncodePushSubscribe(id string, v browserprotocol.PushSubscribe) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypePushSubscribe, id, v)
}
func testEncodeBrowserClientsGet(id string, v browserprotocol.BrowserClientsGet) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeBrowserClientsGet, id, v)
}
func testEncodeBrowserClientRevoke(id string, v browserprotocol.BrowserClientRevoke) ([]byte, error) {
	return testProtocolEncode(browserprotocol.TypeBrowserClientRevoke, id, v)
}
func testEncodeTerminalInput(id [16]byte, sequence, generation uint64, payload []byte) ([]byte, error) {
	return testProtocolEncodeTerminal(browserprotocol.TerminalFrame{Opcode: browserprotocol.TerminalInputOpcode, SessionID: id, Sequence: sequence, LeaseGeneration: generation, Payload: payload})
}
func testDecodeServerControl(data []byte) (browserprotocol.ControlFrame, error) {
	return testProtocolDecode(data, 2)
}
