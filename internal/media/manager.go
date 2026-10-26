// Package media 提供媒体文件上传、验证、处理、命名策略、Imgproxy 防篡改签名与多态实体附件管理。
package media

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"mime/multipart"
	"path/filepath"
	"strings"

	"github.com/gabriel-vasile/mimetype"
	"github.com/jackc/pgx/v5"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/pkg/errs"
	"cgin/pkg/logger"
	"cgin/pkg/storage"
)

const (
	// maxAvatarSize 是头像上传允许的最大体积（2MB）。
	maxAvatarSize = 2 * 1024 * 1024
	// avatarSize 是头像签名 URL 的边长（像素）。
	avatarSize = 256
)

// ErrMediaNotFound 表示未查询到目标媒体记录的哨兵错误。
var ErrMediaNotFound = errors.New("media: record not found")

// Owner 标识媒体附件的多态宿主：业务模型类型与主键。
type Owner struct {
	Type string
	ID   string
}

// NewOwner 构造宿主标识并去除首尾空白。
func NewOwner(modelType, modelID string) Owner {
	return Owner{
		Type: strings.TrimSpace(modelType),
		ID:   strings.TrimSpace(modelID),
	}
}

// valid 报告宿主标识是否完整；缺失主键时不可写入附件。
func (o Owner) valid() bool {
	return o.ID != ""
}

// AttachConfig 保存媒体附加的选项参数。
type AttachConfig struct {
	Name             string         // 原始友好名称（若为空则默认取上传文件名）
	SingleFile       bool           // 是否为单文件集合（若为 true，自动清空旧同名集合下的物理文件与数据库记录）
	CustomProperties map[string]any // 自定义扩展业务属性（JSONB）
	Manipulations    map[string]any // 图片处理指令与参数元数据（JSONB）
	OrderColumn      *int           // 多附件排序号
	DiskName         string         // 显式指定的存储磁盘名称
	link             LinkFunc       // 事务内宿主回写回调（非导出，仅由 WithLink 设置）
}

// AttachOption 定义附加选项函数。
type AttachOption func(*AttachConfig)

// WithLink 登记事务内的宿主回写回调。
func WithLink(link LinkFunc) AttachOption {
	return func(c *AttachConfig) {
		c.link = link
	}
}

// WithName 设置媒体附件的展示名称。
func WithName(name string) AttachOption {
	return func(c *AttachConfig) {
		c.Name = strings.TrimSpace(name)
	}
}

// WithSingleFile 设置该附件集合为单文件模式（自动覆盖已有旧附件）。
func WithSingleFile() AttachOption {
	return func(c *AttachConfig) {
		c.SingleFile = true
	}
}

// WithCustomProperties 设置自定义业务元数据。
func WithCustomProperties(props map[string]any) AttachOption {
	return func(c *AttachConfig) {
		c.CustomProperties = maps.Clone(props)
	}
}

// WithManipulations 设置图片衍生与操作指令。
func WithManipulations(manipulations map[string]any) AttachOption {
	return func(c *AttachConfig) {
		c.Manipulations = maps.Clone(manipulations)
	}
}

// WithOrderColumn 设置多附件排序号。
func WithOrderColumn(order int) AttachOption {
	return func(c *AttachConfig) {
		c.OrderColumn = &order
	}
}

// Manager 为业务层提供多态媒体附件聚合管理门面。
type Manager struct {
	db       *database.DB
	disk     storage.Disk
	pipeline Pipeline
	image    Client
	cfg      config.MediaConfig
}

// NewManager 构建多态媒体管理门面实例。
func NewManager(
	mediaCfg config.MediaConfig,
	disk storage.Disk,
	db *database.DB,
	pipeline Pipeline,
	image Client,
) *Manager {
	return &Manager{
		db:       db,
		disk:     disk,
		pipeline: pipeline,
		image:    image,
		cfg:      mediaCfg,
	}
}

// Ping 执行底层 imgproxy 连通性探测（满足 ready.Pinger 契约）。
func (m *Manager) Ping(ctx context.Context) error {
	if m.image == nil {
		return nil
	}
	return m.image.Ping(ctx)
}

// Driver 返回图像处理驱动标识。
func (m *Manager) Driver() string {
	return lo.Ternary(m.image != nil, m.image.Driver(), "imgproxy")
}

// Enabled 报告多态媒体管理器中的 imgproxy 是否已启用。
func (m *Manager) Enabled() bool {
	return m.image != nil && m.image.Enabled()
}

// LinkFunc 在一个媒体事务内把生成后的访问 URL 回写到宿主模型。
//
// 返回错误即让整笔事务回滚：媒体行与宿主字段要么一起成立，要么都不成立。
// 回调只允许做数据库写入，事务提交后才会执行的回调应改用 database.AfterCommit。
type LinkFunc func(ctx context.Context, media *database.Media) error

// Attach 将文件或流上传并关联至指定业务模型的多态集合中。
func (m *Manager) Attach(
	ctx context.Context,
	owner Owner,
	collection string,
	source any,
	opts ...AttachOption,
) (*database.Media, error) {
	return m.attach(ctx, owner, collection, source, opts...)
}

// attach 是 Attach 的实现，可选地在同一事务内回写宿主模型。
func (m *Manager) attach(
	ctx context.Context,
	owner Owner,
	collection string,
	source any,
	opts ...AttachOption,
) (*database.Media, error) {
	if !owner.valid() {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("media: owner id cannot be empty")
	}

	modelType := owner.Type
	modelID := owner.ID
	collection = cmp.Or(strings.TrimSpace(collection), m.cfg.DefaultCollection, "default")

	var conf AttachConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&conf)
		}
	}
	link := conf.link

	// 1. 上传文件到底层存储并获取元数据
	fileInfo, err := m.uploadSource(ctx, source, collection)
	if err != nil {
		return nil, err
	}

	friendlyName := cmp.Or(conf.Name, fileInfo.OriginalName, fileInfo.FileName)
	params := sqlc.CreateMediaParams{
		ModelType:            modelType,
		ModelID:              modelID,
		CollectionName:       collection,
		Name:                 friendlyName,
		FileName:             fileInfo.Path,
		MIMEType:             fileInfo.MimeType,
		Disk:                 cmp.Or(conf.DiskName, "default"),
		Size:                 fileInfo.Size,
		CustomProperties:     conf.CustomProperties,
		Manipulations:        conf.Manipulations,
		GeneratedConversions: map[string]any{},
	}
	if conf.OrderColumn != nil {
		order := int32(*conf.OrderColumn) //nolint:gosec // 排序号来自受控 int 参数，PostgreSQL integer 范围足够
		params.OrderColumn = &order
	}

	// 2. 单文件模式：删除旧记录与写入新记录在同一事务内完成；
	//    物理文件删除登记为提交后回调，事务回滚时不会误删仍被引用的文件。
	var created database.Media
	err = m.db.RunInTx(ctx, func(txCtx context.Context, q *sqlc.Queries) error {
		if conf.SingleFile {
			removed, err := q.DeleteMediaByModelAndCollection(txCtx, sqlc.DeleteMediaByModelAndCollectionParams{
				ModelType:      modelType,
				ModelID:        modelID,
				CollectionName: collection,
			})
			if err != nil {
				return err
			}
			names := lo.Map(removed, func(item database.Media, _ int) string { return item.FileName })
			database.AfterCommit(txCtx, func() { m.deleteFiles(ctx, names) })
		}

		row, err := q.CreateMedia(txCtx, params)
		if err != nil {
			return err
		}
		created = row

		// 宿主回写与媒体写入共享同一事务：回写失败时媒体行一并回滚。
		if link != nil {
			if err := link(txCtx, &created); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// 入库失败时补偿删除刚上传的物理文件
		if delErr := m.pipeline.Delete(ctx, fileInfo.Path); delErr != nil {
			logger.FromContext(ctx).Warn().
				Err(delErr).
				Str("path", fileInfo.Path).
				Msg("failed to clean up orphaned media file after DB insert failure")
		}
		return nil, oops.Code(errs.CodeInternal).In("media.Manager.Attach").Wrap(err)
	}

	return &created, nil
}

// uploadSource 内部按源类型调度底层管道上传。
func (m *Manager) uploadSource(ctx context.Context, source any, category string) (*FileInfo, error) {
	switch s := source.(type) {
	case *multipart.FileHeader:
		return m.pipeline.Upload(ctx, s, category)
	case []byte:
		return m.pipeline.UploadBytes(ctx, s, "upload.bin", category)
	case *FileInfo:
		return s, nil
	case FileInfo:
		return &s, nil
	default:
		return nil, oops.Code(errs.CodeBadRequest).Errorf("media: unsupported upload source type: %T", source)
	}
}

// clearOldFiles 删除指定模型集合下的数据库记录，并在事务提交后清理物理文件。
func (m *Manager) clearOldFiles(ctx context.Context, modelType, modelID, collection string) error {
	removed, err := m.db.Queries(ctx).DeleteMediaByModelAndCollection(ctx, sqlc.DeleteMediaByModelAndCollectionParams{
		ModelType:      modelType,
		ModelID:        modelID,
		CollectionName: collection,
	})
	if err != nil {
		return err
	}

	names := lo.Map(removed, func(item database.Media, _ int) string { return item.FileName })
	database.AfterCommit(ctx, func() { m.deleteFiles(ctx, names) })
	return nil
}

// deleteFiles 尽力删除物理文件；失败只记日志，不影响数据库已经完成的事实。
func (m *Manager) deleteFiles(ctx context.Context, names []string) {
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := m.disk.Delete(ctx, name); err != nil {
			logger.FromContext(ctx).Warn().Err(err).Str("path", name).Msg("failed to delete media file")
		}
	}
}

// ensureDB 报告数据访问入口是否已装配。
//
// 除构造函数外，所有访问数据库的方法都先过这道守卫：未装配时返回
// CodeDatabaseUnavailable，避免把 nil 指针错误暴露成 500 堆栈。
func (m *Manager) ensureDB() error {
	if m.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("media: database connection is not available")
	}
	return nil
}

// Get 查询指定宿主下某个集合（或全部集合）的所有媒体附件。
func (m *Manager) Get(ctx context.Context, owner Owner, collection ...string) ([]database.Media, error) {
	if !owner.valid() {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("media: owner id cannot be empty")
	}

	if err := m.ensureDB(); err != nil {
		return nil, err
	}

	params := sqlc.ListMediaByModelParams{
		ModelType: owner.Type,
		ModelID:   owner.ID,
	}
	if len(collection) > 0 && strings.TrimSpace(collection[0]) != "" {
		name := strings.TrimSpace(collection[0])
		params.CollectionName = &name
	}

	items, err := m.db.Queries(ctx).ListMediaByModel(ctx, params)
	if err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Manager.Get").Wrap(err)
	}

	return items, nil
}

// GetForOwners 批量查询同一宿主类型下多个宿主 ID 的媒体附件，按 ModelID 分组返回，彻底消除列表页 N+1 查询。
func (m *Manager) GetForOwners(
	ctx context.Context,
	modelType string,
	modelIDs []string,
	collection ...string,
) (map[string][]database.Media, error) {
	if strings.TrimSpace(modelType) == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("media: model type cannot be empty")
	}
	if len(modelIDs) == 0 {
		return map[string][]database.Media{}, nil
	}
	if err := m.ensureDB(); err != nil {
		return nil, err
	}

	params := sqlc.ListMediaByModelsParams{
		ModelType: strings.TrimSpace(modelType),
		ModelIds:  lo.Uniq(modelIDs),
	}
	if len(collection) > 0 && strings.TrimSpace(collection[0]) != "" {
		name := strings.TrimSpace(collection[0])
		params.CollectionName = &name
	}

	items, err := m.db.Queries(ctx).ListMediaByModels(ctx, params)
	if err != nil {
		return nil, oops.Code(errs.CodeInternal).In("media.Manager.GetForOwners").Wrap(err)
	}

	grouped := make(map[string][]database.Media, len(params.ModelIds))
	for _, item := range items {
		grouped[item.ModelID] = append(grouped[item.ModelID], item)
	}
	return grouped, nil
}

// GetFirst 查询指定宿主特定集合下的首个媒体附件（如头像）。
func (m *Manager) GetFirst(ctx context.Context, owner Owner, collection string) (*database.Media, error) {
	if !owner.valid() {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("media: owner id cannot be empty")
	}
	if err := m.ensureDB(); err != nil {
		return nil, err
	}

	item, err := m.db.Queries(ctx).GetFirstMedia(ctx, sqlc.GetFirstMediaParams{
		ModelType:      owner.Type,
		ModelID:        owner.ID,
		CollectionName: collection,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, oops.Code(errs.CodeNotFound).Wrap(ErrMediaNotFound)
		}
		return nil, oops.Code(errs.CodeInternal).In("media.Manager.GetFirst").Wrap(err)
	}
	return &item, nil
}

// FindByUUID 根据对外暴露的安全唯一 UUID 查询媒体附件记录。
func (m *Manager) FindByUUID(ctx context.Context, uuid string) (*database.Media, error) {
	if strings.TrimSpace(uuid) == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("media: uuid cannot be empty")
	}
	if err := m.ensureDB(); err != nil {
		return nil, err
	}

	item, err := m.db.Queries(ctx).GetMediaByUUID(ctx, sqlc.GetMediaByUUIDParams{UUID: strings.TrimSpace(uuid)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, oops.Code(errs.CodeNotFound).Wrap(ErrMediaNotFound)
		}
		return nil, oops.Code(errs.CodeInternal).In("media.Manager.FindByUUID").Wrap(err)
	}
	return &item, nil
}

// FindByID 根据自增物理主键 ID 查询媒体附件记录。
func (m *Manager) FindByID(ctx context.Context, id int64) (*database.Media, error) {
	if err := m.ensureDB(); err != nil {
		return nil, err
	}

	item, err := m.db.Queries(ctx).GetMediaByID(ctx, sqlc.GetMediaByIDParams{ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, oops.Code(errs.CodeNotFound).Wrap(ErrMediaNotFound)
		}
		return nil, oops.Code(errs.CodeInternal).In("media.Manager.FindByID").Wrap(err)
	}
	return &item, nil
}

// Delete 原子删除指定 UUID 的媒体附件记录，并在事务提交后清理物理文件。
func (m *Manager) Delete(ctx context.Context, uuid string) error {
	trimmed := strings.TrimSpace(uuid)
	if trimmed == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("media: uuid cannot be empty")
	}
	if err := m.ensureDB(); err != nil {
		return err
	}

	deleted, err := m.db.Queries(ctx).DeleteMediaByUUID(ctx, sqlc.DeleteMediaByUUIDParams{UUID: trimmed})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return oops.Code(errs.CodeNotFound).Wrap(ErrMediaNotFound)
		}
		return oops.Code(errs.CodeInternal).In("media.Manager.Delete").Wrap(err)
	}

	database.AfterCommit(ctx, func() { m.deleteFiles(ctx, []string{deleted.FileName}) })
	return nil
}

// ClearCollection 清空指定宿主特定集合下的所有媒体附件（物理文件与数据库记录全部清除）。
func (m *Manager) ClearCollection(ctx context.Context, owner Owner, collection string) error {
	if !owner.valid() {
		return oops.Code(errs.CodeBadRequest).Errorf("media: owner id cannot be empty")
	}
	if err := m.ensureDB(); err != nil {
		return err
	}

	return m.clearOldFiles(ctx, owner.Type, owner.ID, strings.TrimSpace(collection))
}

// URL 智能生成媒体文件的访问 URL。
// 如果指定了图像处理选项且配置了 Imgproxy，则生成 Imgproxy 防篡改签名 URL；否则返回 Storage Disk 原始/预签名 URL。
func (m *Manager) URL(mediaItem *database.Media, opts ...ProcessOption) string {
	if mediaItem == nil || mediaItem.FileName == "" {
		return ""
	}

	isImage := strings.HasPrefix(mediaItem.MIMEType, "image/") && mediaItem.MIMEType != "image/svg+xml"
	if len(opts) > 0 && isImage && m.image != nil {
		imgURL, err := m.image.URL(mediaItem.FileName, opts...)
		if err == nil && imgURL != "" {
			return imgURL
		}
	}

	url, _ := m.disk.URL(mediaItem.FileName)
	return url
}

// Thumbnail 快捷获取多态媒体文件的缩略图 URL。
func (m *Manager) Thumbnail(mediaItem *database.Media, width, height int) string {
	return m.URL(mediaItem,
		WithResizingType(ResizeFill),
		WithResize(width, height),
	)
}

// Avatar 快捷获取多态媒体文件的头像裁切 URL。
func (m *Manager) Avatar(mediaItem *database.Media, size int) string {
	return m.URL(mediaItem,
		WithResizingType(ResizeFill),
		WithResize(size, size),
		WithGravity(GravityFocusPoint),
	)
}

// ValidateAvatarFile 校验头像文件的大小、扩展名与真实魔数。
func ValidateAvatarFile(file *multipart.FileHeader) error {
	if file == nil || file.Size <= 0 {
		return errs.BadRequest("Avatar file cannot be empty")
	}
	if file.Size > maxAvatarSize {
		return errs.BadRequest("Avatar file size must not exceed 2MB")
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file.Filename), "."))
	switch ext {
	case "jpg", "jpeg", "png", "webp":
	default:
		return errs.BadRequest("Invalid image extension; only jpg, jpeg, png, and webp are allowed")
	}

	src, err := file.Open()
	if err != nil {
		return errs.BadRequest("Failed to open uploaded file")
	}
	defer func() { _ = src.Close() }()

	header := make([]byte, 512)
	n, err := io.ReadFull(src, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return errs.BadRequest("Failed to read uploaded file")
	}

	mtype := mimetype.Detect(header[:n])
	switch mtype.String() {
	case "image/jpeg", "image/png", "image/webp":
		return nil
	default:
		return errs.BadRequest("Invalid image format; only JPEG, PNG, and WebP are allowed")
	}
}

// ChangeAvatar 校验头像规格（魔数嗅探、格式白名单、尺寸限制），以单文件模式
// 原子替换用户头像，并在同一事务内通过 link 把访问 URL 回写给宿主模型。
//
// link 为 nil 时只替换媒体记录，不改宿主字段。
func (m *Manager) ChangeAvatar(
	ctx context.Context,
	userID string,
	file *multipart.FileHeader,
	link func(ctx context.Context, url string) error,
) (string, error) {
	if err := ValidateAvatarFile(file); err != nil {
		return "", err
	}
	if m == nil {
		return "", oops.Code(errs.CodeInternal).Errorf("media: manager is not configured")
	}

	var url string
	record, err := m.attach(
		ctx,
		NewOwner("user", userID),
		"avatar",
		file,
		WithSingleFile(),
		WithLink(func(txCtx context.Context, media *database.Media) error {
			url = cmp.Or(m.Avatar(media, avatarSize), m.URL(media), media.FileName)
			if link == nil {
				return nil
			}
			return link(txCtx, url)
		}),
	)
	if err != nil {
		return "", err
	}

	return cmp.Or(url, m.Avatar(record, avatarSize), m.URL(record), record.FileName), nil
}
