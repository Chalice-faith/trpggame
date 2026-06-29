package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// UploadScript 上传 PDF 剧本
func UploadScript(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - UploadScript",
	})
}

// ListScripts 剧本列表
func ListScripts(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ListScripts",
	})
}

// GetScriptDetail 剧本详情
func GetScriptDetail(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - GetScriptDetail",
	})
}

// DeleteScript 删除剧本
func DeleteScript(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - DeleteScript",
	})
}
