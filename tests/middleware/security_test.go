package middleware_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"cgin/internal/middleware/global"
)

func TestEnforceJSONContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{})

	router := gin.New()
	router.Use(m.ErrorHandler(), m.EnforceJSONContentType())
	router.POST("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	t.Run("Valid JSON passes", func(t *testing.T) {
		body := []byte(`{"key":"val"}`)
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Valid JSON with charset passes", func(t *testing.T) {
		body := []byte(`{"key":"val"}`)
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Empty body passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Multipart form passes", func(t *testing.T) {
		body := []byte(`dummy form data`)
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=something")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("Invalid text/plain fails", func(t *testing.T) {
		body := []byte(`plain text`)
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
