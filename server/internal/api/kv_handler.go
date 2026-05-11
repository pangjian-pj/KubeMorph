package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type KVHandler struct {
	logger *zap.Logger
	store  KVHandlerDepKV
}

func NewKVHandler(logger *zap.Logger, store KVHandlerDepKV) *KVHandler {
	return &KVHandler{logger: logger, store: store}
}

type kvCreateRequest struct {
	Key   string `json:"key" binding:"required"`
	Value string `json:"value" binding:"required"`
}

type kvUpdateRequest struct {
	Value string `json:"value" binding:"required"`
}

func (h *KVHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/kv", h.create)
	rg.GET("/kv/:key", h.get)
	rg.PUT("/kv/:key", h.update)
	rg.DELETE("/kv/:key", h.delete)
}

func (h *KVHandler) create(c *gin.Context) {
	var req kvCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.store.Put(c, req.Key, req.Value); err != nil {
		h.logger.Error("kv put failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store put failed"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"key": req.Key})
}

func (h *KVHandler) get(c *gin.Context) {
	key := c.Param("key")
	value, ok, err := h.store.Get(c, key)
	if err != nil {
		h.logger.Error("kv get failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store get failed"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": key, "value": value})
}

func (h *KVHandler) update(c *gin.Context) {
	key := c.Param("key")
	var req kvUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.store.Put(c, key, req.Value); err != nil {
		h.logger.Error("kv update failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store put failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": key})
}

func (h *KVHandler) delete(c *gin.Context) {
	key := c.Param("key")
	deleted, err := h.store.Delete(c, key)
	if err != nil {
		h.logger.Error("kv delete failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store delete failed"})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.Status(http.StatusNoContent)
}
