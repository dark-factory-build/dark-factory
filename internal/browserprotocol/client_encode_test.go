package browserprotocol

func EncodePairProve(id string, value PairProve) ([]byte, error) {
	return encodeControl(TypePairProve, id, value)
}
func EncodeAuthProve(id string, value AuthProve) ([]byte, error) {
	return encodeControl(TypeAuthProve, id, value)
}
func EncodeStateGet(id string, value StateGet) ([]byte, error) {
	return encodeControl(TypeStateGet, id, value)
}
func EncodeStateWatch(id string, value StateWatch) ([]byte, error) {
	return encodeControl(TypeStateWatch, id, value)
}
func EncodeHumanRequestDetailGet(id string, value HumanRequestDetailGet) ([]byte, error) {
	return encodeControl(TypeHumanRequestDetailGet, id, value)
}
func DecodeServerControl(data []byte) (ControlFrame, error) { return decodeControl(data, serverRole) }
func EncodeTaskListGet(id string, value TaskListGet) ([]byte, error) {
	return encodeControl(TypeTaskListGet, id, value)
}
func EncodeAgentControl(id string, value AgentControl) ([]byte, error) {
	return encodeControl(TypeAgentControl, id, value)
}
func EncodeTaskHistoryGet(id string, value TaskHistoryGet) ([]byte, error) {
	return encodeControl(TypeTaskHistoryGet, id, value)
}
func EncodeTaskDetailGet(id string, value TaskDetailGet) ([]byte, error) {
	return encodeControl(TypeTaskDetailGet, id, value)
}
func EncodeTaskAttachment(id string, value TaskAttachmentChunk) ([]byte, error) {
	return encodeControl(TypeTaskAttachment, id, value)
}
func EncodeTaskEnqueue(id string, value TaskEnqueue) ([]byte, error) {
	return encodeControl(TypeTaskEnqueue, id, value)
}
func EncodeProjectContent(id string, value ProjectContent) ([]byte, error) {
	return encodeControl(TypeProjectContent, id, value)
}
func EncodeHumanRequestReply(id string, value HumanRequestReply) ([]byte, error) {
	return encodeControl(TypeHumanRequestReply, id, value)
}
func EncodeHumanRequestCancelRun(id string, value HumanRequestCancelRun) ([]byte, error) {
	return encodeControl(TypeHumanRequestCancelRun, id, value)
}
func EncodeTerminalAttach(id string, value TerminalAttach) ([]byte, error) {
	return encodeControl(TypeTerminalAttach, id, value)
}
func EncodeTerminalTargetGet(id string, value TerminalTargetGet) ([]byte, error) {
	return encodeControl(TypeTerminalTargetGet, id, value)
}
func EncodeTerminalAck(value TerminalAck) ([]byte, error) {
	return encodeControl(TypeTerminalAck, "", value)
}
func EncodeTerminalLeaseAcquire(id string, value TerminalLeaseAcquire) ([]byte, error) {
	return encodeControl(TypeTerminalLeaseAcquire, id, value)
}
func EncodeTerminalLeaseRenew(id string, value TerminalLeaseRenew) ([]byte, error) {
	return encodeControl(TypeTerminalLeaseRenew, id, value)
}
func EncodeTerminalDetach(id string, value TerminalDetach) ([]byte, error) {
	return encodeControl(TypeTerminalDetach, id, value)
}
func EncodeRemoteInvite(id string, value RemoteInvite) ([]byte, error) {
	return encodeControl(TypeRemoteInvite, id, value)
}
func EncodePushSubscribe(id string, value PushSubscribe) ([]byte, error) {
	return encodeControl(TypePushSubscribe, id, value)
}
func EncodeBrowserClientsGet(id string, value BrowserClientsGet) ([]byte, error) {
	return encodeControl(TypeBrowserClientsGet, id, value)
}
func EncodeBrowserClientRevoke(id string, value BrowserClientRevoke) ([]byte, error) {
	return encodeControl(TypeBrowserClientRevoke, id, value)
}
func EncodeTerminalInput(sessionID [16]byte, sequence, generation uint64, payload []byte) ([]byte, error) {
	return encodeTerminalFrame(TerminalFrame{Opcode: TerminalInputOpcode, SessionID: sessionID, Sequence: sequence, LeaseGeneration: generation, Payload: payload})
}
