package repo

import (
	"context"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"trpggame/internal/model"
)

// UserRepo 用户数据访问层
type UserRepo struct {
	db *gorm.DB
}

// FindActiveByID 查询未删除用户。
func (r *UserRepo) FindActiveByID(ctx context.Context, id uint) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("deleted_at IS NULL").First(&user, id).Error
	return &user, err
}

// FindActiveByIDs 批量查询未删除用户。
func (r *UserRepo) FindActiveByIDs(ctx context.Context, ids []uint) ([]model.User, error) {
	if len(ids) == 0 {
		return []model.User{}, nil
	}
	var users []model.User
	err := r.db.WithContext(ctx).Where("id IN ? AND deleted_at IS NULL", ids).Find(&users).Error
	return users, err
}

// SearchActive 按精确 ID、精确用户名或昵称前缀搜索，结果最多 20 条。
func (r *UserRepo) SearchActive(ctx context.Context, currentUserID uint, keyword string) ([]model.User, error) {
	keyword = strings.TrimSpace(keyword)
	query := r.db.WithContext(ctx).Where("id <> ? AND deleted_at IS NULL", currentUserID)
	numericID, numericErr := strconv.ParseUint(keyword, 10, 64)
	if numericErr == nil && numericID > 0 {
		query = query.Where("id = ? OR username = ?", numericID, keyword).
			Order("CASE WHEN id = " + strconv.FormatUint(numericID, 10) + " THEN 0 ELSE 1 END, id")
	} else if len([]rune(keyword)) >= 2 {
		escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(keyword)
		query = query.Where("username = ? OR nickname LIKE ? ESCAPE '\\\\'", keyword, escaped+"%").
			Order(clause.Expr{SQL: "CASE WHEN username = ? THEN 0 ELSE 1 END, id", Vars: []any{keyword}, WithoutParentheses: true})
	} else {
		query = query.Where("username = ?", keyword).Order("id")
	}
	var users []model.User
	return users, query.Limit(20).Find(&users).Error
}

// NewUserRepo 创建 UserRepo
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

// Create 创建用户
func (r *UserRepo) Create(user *model.User) error {
	return r.db.Create(user).Error
}

// FindByID 按 ID 查询
func (r *UserRepo) FindByID(id uint) (*model.User, error) {
	var user model.User
	err := r.db.First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByUsername 按用户名查询
func (r *UserRepo) FindByUsername(username string) (*model.User, error) {
	var user model.User
	err := r.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByEmail 按邮箱查询
func (r *UserRepo) FindByEmail(email string) (*model.User, error) {
	var user model.User
	err := r.db.Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Update 更新用户
func (r *UserRepo) Update(user *model.User) error {
	return r.db.Save(user).Error
}
