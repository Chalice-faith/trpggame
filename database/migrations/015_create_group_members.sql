-- 015_create_group_members.sql
-- M2.3 群成员、角色与离群状态；不创建外键，和 conversation_members 同事务镜像。

CREATE TABLE IF NOT EXISTS group_members (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    group_id            BIGINT UNSIGNED NOT NULL,
    user_id             BIGINT UNSIGNED NOT NULL,
    role                VARCHAR(16) NOT NULL DEFAULT 'member',
    status              VARCHAR(16) NOT NULL DEFAULT 'active',
    joined_at           DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    left_at             DATETIME(3),
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                        ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_group_members_group_user (group_id, user_id),
    KEY idx_group_members_user_status_group (user_id, status, group_id),
    KEY idx_group_members_group_status_role_user (group_id, status, role, user_id),
    CONSTRAINT chk_group_members_role CHECK (role IN ('member', 'admin', 'owner')),
    CONSTRAINT chk_group_members_status CHECK (status IN ('active', 'left'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
