package config

import (
	"strings"

	"github.com/spf13/viper"
)

// Config 应用配置根结构
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Redis    RedisConfig
	JWT      JWTConfig
	AI       AIConfig
	MinIO    MinIOConfig
	Internal InternalConfig `mapstructure:"internal"`
}

// ServerConfig HTTP 服务配置
type ServerConfig struct {
	Port string
	Mode string // debug | release | test
}

// DatabaseConfig MySQL 配置
type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	Charset  string
	Loc      string
}

// RedisConfig Redis 配置
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// JWTConfig JWT 配置
type JWTConfig struct {
	Secret          string
	AccessTokenTTL  int // 分钟
	RefreshTokenTTL int // 小时
}

// AIConfig Python AI 服务配置
type AIConfig struct {
	BaseURL string
	Timeout int // 秒
}

// MinIOConfig MinIO 对象存储配置
type MinIOConfig struct {
	Endpoint      string
	AccessKey     string
	SecretKey     string
	Bucket        string
	UseSSL        bool
	MaxUploadSize int64 // 字节
}

// InternalConfig 服务间内部接口配置。
type InternalConfig struct {
	SharedSecret string `mapstructure:"shared_secret"`
}

// Load 加载配置，优先级：环境变量 > config.yaml > 默认值
func Load() (*Config, error) {
	v := viper.New()

	// 配置文件搜索
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./configs")

	// 环境变量映射
	v.SetEnvPrefix("TRPG")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 设置默认值
	setDefaults(v)

	// 读取配置文件（不存在也不报错，依赖默认值 + 环境变量）
	_ = v.ReadInConfig()

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	// Server
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.mode", "debug")

	// Database
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", "3306")
	v.SetDefault("database.user", "trpg")
	v.SetDefault("database.password", "trpg123")
	v.SetDefault("database.dbname", "trpggame")
	v.SetDefault("database.charset", "utf8mb4")
	v.SetDefault("database.loc", "Local")

	// Redis
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)

	// JWT
	v.SetDefault("jwt.secret", "change-me-in-production")
	v.SetDefault("jwt.accesstokenttl", 15)
	v.SetDefault("jwt.refreshtokenttl", 168) // 7 days

	// AI
	v.SetDefault("ai.baseurl", "http://localhost:8000")
	v.SetDefault("ai.timeout", 60)

	// MinIO
	v.SetDefault("minio.endpoint", "localhost:9000")
	v.SetDefault("minio.accesskey", "minioadmin")
	v.SetDefault("minio.secretkey", "minioadmin")
	v.SetDefault("minio.bucket", "trpg-scripts")
	v.SetDefault("minio.usessl", false)
	v.SetDefault("minio.maxuploadsize", int64(50<<20)) // 50 MiB

	// Internal API
	v.SetDefault("internal.shared_secret", "dev-internal-secret-change-in-production")
}

// DSN 返回 MySQL 连接字符串
func (d *DatabaseConfig) DSN() string {
	return d.User + ":" + d.Password +
		"@tcp(" + d.Host + ":" + d.Port + ")/" + d.DBName +
		"?charset=" + d.Charset +
		"&parseTime=True&loc=" + d.Loc
}
