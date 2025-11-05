package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultImageURL = "https://raw.githubusercontent.com/Mistium/Origin-OS/main/Resources/no-pfp.jpeg"
	cacheTimeout    = 3600 // 1 hour
	port            = "5604"
)

var (
	documentPath         = filepath.Join(os.Getenv("HOME"), "Documents")
	defaultImageContent  []byte
	defaultImageEtag     string
	defaultBannerContent []byte

	roundedCache = make(map[string]CachedImage)
	resizedCache = make(map[string]CachedImage)
	cacheMutex   sync.RWMutex
)

type CachedImage struct {
	Data        []byte
	ContentType string
	Etag        string
	Timestamp   time.Time
}

type User struct {
	Username     string `json:"username"`
	Key          string `json:"key"`
	MaxSize      any    `json:"max_size"`
	Subscription any    `json:"sys.subscription"`
}

func (u User) GetSubscription() string {
	if strings.EqualFold(u.Username, "mist") {
		// keep me as the sigma
		return "Max"
	}
	usub := u.Subscription
	val := "Free"

	sub, ok := usub.(map[string]any)
	if !ok {
		return val
	}
	val = getStringOrDefault(sub["tier"], "Free")
	return val
}

type UploadRequest struct {
	Image string `json:"image"`
	Token string `json:"token"`
}

func init() {
	loadDefaultImage()
	loadDefaultBanner()
}

func requiresAdmin(c *gin.Context) {
	token := c.Query("ADMIN_TOKEN")
	if token == ADMIN_TOKEN {
		c.Next()
		return
	}
	c.JSON(401, gin.H{"error": "Unauthorized"})
	c.Abort()
}

func main() {
	envOnce.Do(loadEnvFile)
	gin.SetMode(gin.ReleaseMode)

	r := gin.Default()

	r.Use(enableCORS())

	r.GET("/:username", avatarHandler)
	r.GET("/.banners/:username", bannerHandler)
	r.POST("/rotur-upload-pfp", requiresAdmin, uploadPfpHandler)
	r.POST("/rotur-upload-banner", requiresAdmin, uploadBannerHandler)

	log.Printf("Avatar service starting on port %s", port)
	r.Run(":" + port)
}
