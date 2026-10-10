package imws

import (
	"context"
	"errors"
	"time"
)

// businessHandleTimeout 约束单条业务消息的处理时长，显著短于 Pong 窗口，
// 处理结束后 readPump 立即恢复读取控制帧，见 M2.2 实施方案 13。
const businessHandleTimeout = 5 * time.Second

// InboundHandler 处理 imws 信封承载的业务消息。实现方在事务提交后通过
// 事件发布接口向其他用户推送实时事件；返回的消息只回给当前连接。
type InboundHandler interface {
	HandleIM(ctx context.Context, userID uint, message ClientMessage) (*ServerMessage, error)
}

// classifyBusinessError 将业务处理错误映射为错误帧载荷；未知错误统一回落到 1716。
func classifyBusinessError(err error) (int, string) {
	var business *BusinessError
	if errors.As(err, &business) {
		return business.Code, business.Message
	}
	return ErrorCodeChatUnavailable, "chat unavailable"
}

// dispatchBusiness 同步处理一条业务消息并按读取顺序回写结果。
func (c *Client) dispatchBusiness(message *ClientMessage) {
	var handler InboundHandler
	if c.Hub != nil {
		handler = c.Hub.InboundHandler()
	}
	if handler == nil {
		c.sendError(ErrorCodeUnsupportedMessageType, "unsupported message type", message.RequestID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessHandleTimeout)
	defer cancel()

	reply, err := handler.HandleIM(ctx, c.UserID, *message)
	if err != nil {
		code, text := classifyBusinessError(err)
		c.sendError(code, text, message.RequestID)
		return
	}
	if reply == nil {
		return
	}
	timestamp := reply.Timestamp
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	payload, marshalErr := MarshalServerMessage(reply.Type, reply.RequestID, timestamp, reply.Data)
	if marshalErr != nil {
		c.sendError(ErrorCodeChatUnavailable, "chat unavailable", message.RequestID)
		return
	}
	if c.Hub != nil {
		c.Hub.sendToClient(c, payload)
	}
}
