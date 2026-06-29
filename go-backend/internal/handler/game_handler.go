package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// StartSoloGame 单人快速开始
func StartSoloGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - StartSoloGame",
	})
}

// SubmitAction 提交玩家行动
func SubmitAction(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - SubmitAction",
	})
}

// ManualSave 手动存档
func ManualSave(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ManualSave",
	})
}

// ListSaves 存档列表
func ListSaves(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ListSaves",
	})
}

// LoadGame 读档恢复
func LoadGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - LoadGame",
	})
}

// PauseGame 暂停游戏
func PauseGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - PauseGame",
	})
}

// ResumeGame 恢复游戏
func ResumeGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ResumeGame",
	})
}

// EndGame 结束游戏
func EndGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - EndGame",
	})
}
