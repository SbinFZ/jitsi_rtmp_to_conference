package colibri

// Bridge is the common interface for both colibri-ws and SCTP datachannel transports.
type Bridge interface {
	SendJSON(payload any) error
	SendRaw(to string, payload []byte) error
	SendEndpointMessage(to string, extras map[string]any) error
	SendLastN(n int) error
	SendReceiverVideoConstraints(constraints map[string]any) error
	SendVideoType(videoType string) error
	TrySendJSON(payload any) error
	TrySendRaw(to string, payload []byte) error
	SendQueueDepth() int
	CanSend() bool
	Messages() <-chan Message
	EnableRawMode()
	RawFrames() <-chan []byte
	Close() error
}
