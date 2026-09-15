package service

import (
	"errors"
	"net/http"

	"trpggame/internal/imws"
)

// ChatErrorDescriptor 是 REST 与 WebSocket 共用的聊天错误安全映射。
// HTTPStatus 供 REST 使用；WebSocket 只使用 Code 与 Message。
type ChatErrorDescriptor struct {
	HTTPStatus int
	Code       int
	Message    string
	Internal   bool
}

func DescribeChatError(err error) ChatErrorDescriptor {
	switch {
	case errors.Is(err, ErrInvalidConversationRequest):
		return ChatErrorDescriptor{HTTPStatus: http.StatusBadRequest, Code: 1708, Message: "invalid conversation request"}
	case errors.Is(err, ErrConversationNotFound):
		return ChatErrorDescriptor{HTTPStatus: http.StatusNotFound, Code: imws.ErrorCodeConversationNotFound, Message: "conversation not found"}
	case errors.Is(err, ErrFriendshipRequired):
		return ChatErrorDescriptor{HTTPStatus: http.StatusConflict, Code: imws.ErrorCodeFriendshipRequired, Message: "friendship required"}
	case errors.Is(err, ErrInvalidChatMessage):
		return ChatErrorDescriptor{HTTPStatus: http.StatusBadRequest, Code: imws.ErrorCodeInvalidChatMessage, Message: "invalid chat message"}
	case errors.Is(err, ErrInvalidMessageContent):
		return ChatErrorDescriptor{HTTPStatus: http.StatusBadRequest, Code: imws.ErrorCodeInvalidMessageContent, Message: "invalid message content"}
	case errors.Is(err, ErrInvalidMessageQuery):
		return ChatErrorDescriptor{HTTPStatus: http.StatusBadRequest, Code: 1713, Message: "invalid message query"}
	case errors.Is(err, ErrReadSequenceConflict):
		return ChatErrorDescriptor{HTTPStatus: http.StatusConflict, Code: 1714, Message: "read sequence conflict"}
	case errors.Is(err, ErrInvalidSyncRequest):
		return ChatErrorDescriptor{HTTPStatus: http.StatusBadRequest, Code: imws.ErrorCodeInvalidSyncRequest, Message: "invalid sync request"}
	default:
		return ChatErrorDescriptor{
			HTTPStatus: http.StatusInternalServerError,
			Code:       imws.ErrorCodeChatUnavailable,
			Message:    "chat unavailable",
			Internal:   true,
		}
	}
}
