// Package models 定义与数据库表一一对应的 bun 模型。
package models

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Media 映射通用多态媒体资源附件表 (media)。
type Media struct {
	bun.BaseModel `bun:"table:media,alias:m"`

	ID                   int64          `bun:"id,pk,autoincrement" json:"id"`
	ModelType            string         `bun:"model_type,notnull" json:"modelType"`
	ModelID              string         `bun:"model_id,notnull" json:"modelId"`
	UUID                 string         `bun:"uuid,notnull" json:"uuid"`
	CollectionName       string         `bun:"collection_name,notnull" json:"collectionName"`
	Name                 string         `bun:"name,notnull" json:"name"`
	FileName             string         `bun:"file_name,notnull" json:"fileName"`
	MimeType             string         `bun:"mime_type,notnull" json:"mimeType"`
	Disk                 string         `bun:"disk,notnull" json:"disk"`
	ConversionsDisk      *string        `bun:"conversions_disk" json:"conversionsDisk,omitempty"`
	Size                 int64          `bun:"size,notnull,default:0" json:"size"`
	ConversionStatus     string         `bun:"conversion_status,notnull,default:'completed'" json:"conversionStatus"`
	OrderColumn          *int           `bun:"order_column" json:"orderColumn,omitempty"`
	Manipulations        map[string]any `bun:"manipulations,type:jsonb" json:"manipulations,omitempty"`
	CustomProperties     map[string]any `bun:"custom_properties,type:jsonb" json:"customProperties,omitempty"`
	GeneratedConversions map[string]any `bun:"generated_conversions,type:jsonb,notnull,default:'{}'" json:"generatedConversions"`
	ResponsiveImages     map[string]any `bun:"responsive_images,type:jsonb" json:"responsiveImages,omitempty"`
	CreatedAt            time.Time      `bun:"created_at,notnull" json:"createdAt"`
	UpdatedAt            time.Time      `bun:"updated_at,notnull" json:"updatedAt"`
}

// BeforeAppendModel 在写入前生成 UUIDv7 并刷新时间戳。
func (m *Media) BeforeAppendModel(_ context.Context, query bun.Query) error {
	now := time.Now().UTC()

	switch query.(type) {
	case *bun.InsertQuery:
		if m.UUID == "" {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			m.UUID = id.String()
		}
		if m.ConversionStatus == "" {
			m.ConversionStatus = "completed"
		}
		if m.GeneratedConversions == nil {
			m.GeneratedConversions = make(map[string]any)
		}
		m.CreatedAt = now
		m.UpdatedAt = now
	case *bun.UpdateQuery:
		m.UpdatedAt = now
	}
	return nil
}
