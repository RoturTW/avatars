package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

type LRUCache struct {
	mu       sync.RWMutex
	items    map[string]*cacheEntry
	maxSize  int
	maxBytes int64
	curBytes int64
}

type cacheEntry struct {
	data       CachedImage
	size       int64
	prev, next *cacheEntry
	key        string
}

type subscriptionCacheEntry struct {
	tier      string
	timestamp time.Time
}

var (
	transformCache    = NewLRUCache(500, 100*1024*1024)
	subscriptionCache sync.Map
	cacheExpiry       = 24 * time.Hour
)

func NewLRUCache(maxSize int, maxBytes int64) *LRUCache {
	return &LRUCache{
		items:    make(map[string]*cacheEntry),
		maxSize:  maxSize,
		maxBytes: maxBytes,
	}
}

func (c *LRUCache) Get(key string) (CachedImage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.items[key]
	if !ok {
		return CachedImage{}, false
	}
	return entry.data, true
}

func (c *LRUCache) Set(key string, img CachedImage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	size := int64(len(img.Data))

	for (len(c.items) >= c.maxSize || c.curBytes+size > c.maxBytes) && len(c.items) > 0 {
		c.evictOldest()
	}

	entry := &cacheEntry{
		data: img,
		size: size,
		key:  key,
	}
	c.items[key] = entry
	c.curBytes += size
}

func (c *LRUCache) evictOldest() {
	for k, v := range c.items {
		delete(c.items, k)
		c.curBytes -= v.size
		break
	}
}

func (c *LRUCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*cacheEntry)
	c.curBytes = 0
}

var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

func getUserSubscriptionTier(username string) string {
	username = strings.ToLower(username)

	if cached, ok := subscriptionCache.Load(username); ok {
		if entry, ok := cached.(subscriptionCacheEntry); ok {
			if time.Since(entry.timestamp) < cacheExpiry {
				return entry.tier
			}
			subscriptionCache.Delete(username)
		}
	}

	usersFile, err := os.ReadFile("users.json")
	if err != nil {
		subscriptionCache.Store(username, subscriptionCacheEntry{tier: "Free", timestamp: time.Now()})
		return "Free"
	}
	var users []User
	if err := json.Unmarshal(usersFile, &users); err != nil {
		subscriptionCache.Store(username, subscriptionCacheEntry{tier: "Free", timestamp: time.Now()})
		return "Free"
	}
	for i := range users {
		if strings.EqualFold(users[i].Username, username) {
			tier := toString(users[i].GetSubscription())
			subscriptionCache.Store(username, subscriptionCacheEntry{tier: tier, timestamp: time.Now()})
			return tier
		}
	}
	subscriptionCache.Store(username, subscriptionCacheEntry{tier: "Free", timestamp: time.Now()})
	return "Free"
}

func decodeFirstGIFFrame(data []byte) (image.Image, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(g.Image) == 0 {
		return nil, fmt.Errorf("no frames in GIF")
	}

	b := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	dst := image.NewRGBA(b)

	bg := &image.Uniform{C: image.Transparent}
	if g.BackgroundIndex < byte(len(g.Image[0].Palette)) {
		bg = &image.Uniform{C: g.Image[0].Palette[g.BackgroundIndex]}
	}
	draw.Draw(dst, b, bg, image.Point{}, draw.Src)

	frame := g.Image[0]
	draw.Draw(dst, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
	return dst, nil
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 {
		quality = 85
	}
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	err := jpeg.Encode(buf, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, err
	}

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

func deleteAvatars(username string) error {
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(avatarDir, base+ext)
		_ = os.Remove(filePath)
	}
	return nil
}

func getAvatarMetadata(username string) (string, string, string, error) {
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(avatarDir, base+ext)
		info, err := os.Stat(filePath)
		if err == nil {
			contentType := "image/jpeg"
			if ext == ".gif" {
				contentType = "image/gif"
			}
			etag := fmt.Sprintf("%s-%d", username, info.ModTime().Unix())
			return filePath, contentType, etag, nil
		}
	}

	return "", "", "", os.ErrNotExist
}

func avatarHandler(c *gin.Context) {
	username, _ := strings.CutSuffix(strings.ToLower(c.Param("username")), ".gif")
	radius := c.Query("radius")
	sizeStr := c.Query("s")

	clientEtag := c.GetHeader("If-None-Match")

	filePath, contentType, baseEtag, metaErr := getAvatarMetadata(username)

	tier := strings.ToLower(toString(getUserSubscriptionTier(username)))
	isPro := slices.Contains([]string{"drive", "pro", "max"}, tier)

	forceFirstFrameJpeg := !isPro && metaErr == nil && contentType == "image/gif"

	finalEtagBase := baseEtag
	if metaErr != nil {
		contentType = "image/jpeg"
		finalEtagBase = defaultImageEtag
	}

	var cacheKeyBuilder strings.Builder
	cacheKeyBuilder.WriteString(finalEtagBase)

	if sizeStr != "" {
		cacheKeyBuilder.WriteString("-size=")
		cacheKeyBuilder.WriteString(sizeStr)
	}
	if radius != "" {
		cacheKeyBuilder.WriteString("-radius=")
		cacheKeyBuilder.WriteString(radius)
	}
	if forceFirstFrameJpeg {
		cacheKeyBuilder.WriteString("-firstframe-jpg")
		contentType = "image/jpeg"
	}

	cacheKey := cacheKeyBuilder.String()
	modifier := sizeStr != "" || radius != ""

	if !modifier && metaErr == nil && !forceFirstFrameJpeg {
		if clientEtag == fmt.Sprintf(`"%s"`, finalEtagBase) {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("ETag", fmt.Sprintf(`"%s"`, finalEtagBase))
		c.Header("Content-Type", contentType)
		c.Header("Cache-Control", "public, max-age=0, must-revalidate")
		if c.Request.Method == http.MethodHead {
			c.Status(200)
			return
		}
		c.File(filePath)
		return
	}

	if c.Request.Method == http.MethodHead {
		c.Header("Content-Type", contentType)
		c.Header("Cache-Control", "public, max-age=0, must-revalidate")
		c.Header("ETag", fmt.Sprintf(`"%s"`, cacheKey))
		c.Status(200)
		return
	}

	cached, ok := transformCache.Get(cacheKey)
	if ok {
		if clientEtag == fmt.Sprintf(`"%s"`, cacheKey) {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("ETag", fmt.Sprintf(`"%s"`, cacheKey))
		c.Header("Cache-Control", "public, max-age=0, must-revalidate")
		c.Data(http.StatusOK, cached.ContentType, cached.Data)
		return
	}

	var imageData []byte
	if metaErr != nil {
		imageData = defaultImageContent
		contentType = "image/jpeg"
	} else {
		var err error
		imageData, err = os.ReadFile(filePath)
		if err != nil {
			imageData = defaultImageContent
			contentType = "image/jpeg"
			finalEtagBase = defaultImageEtag
		}
	}

	if forceFirstFrameJpeg {
		img, err := decodeFirstGIFFrame(imageData)
		if err == nil {
			encoded, err := encodeJPEG(img, 85)
			if err == nil {
				imageData = encoded
				contentType = "image/jpeg"
			}
		}
	}

	if contentType == "image/gif" {
		if sizeStr != "" {
			sz, err := strconv.Atoi(sizeStr)
			if err == nil && sz > 0 && sz <= 256 {
				resizedData, err := resizeGIF(imageData, sz, sz)
				if err == nil {
					imageData = resizedData
				}
			}
		}

		if radius != "" {
			radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
			if err == nil && radiusInt > 0 {
				src, err := gif.DecodeAll(bytes.NewReader(imageData))
				if err == nil {
					rounded, err := roundGIF(src, radiusInt)
					if err == nil {
						buf := bufferPool.Get().(*bytes.Buffer)
						buf.Reset()
						defer bufferPool.Put(buf)

						err = gif.EncodeAll(buf, rounded)
						if err == nil {
							result := make([]byte, buf.Len())
							copy(result, buf.Bytes())
							imageData = result
						}
					}
				}
			}
		}

		transformCache.Set(cacheKey, CachedImage{ContentType: "image/gif", Data: imageData})

		if clientEtag == fmt.Sprintf(`"%s"`, cacheKey) {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("Content-Type", "image/gif")
		c.Header("Cache-Control", "public, max-age=0, must-revalidate")
		c.Header("ETag", fmt.Sprintf(`"%s"`, cacheKey))
		c.Data(http.StatusOK, "image/gif", imageData)
		return
	}

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
		return
	}

	if sizeStr != "" {
		sz, err := strconv.Atoi(sizeStr)
		if err == nil && sz > 0 && sz <= 256 {
			resized := resize.Resize(uint(sz), 0, img, resize.Lanczos3)
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufferPool.Put(buf)

			jpeg.Encode(buf, resized, &jpeg.Options{Quality: 85})
			result := make([]byte, buf.Len())
			copy(result, buf.Bytes())
			imageData = result
		}
	}

	if radius != "" {
		radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
		if err == nil && radiusInt > 0 {
			rounded, newContentType, err := roundCorners(imageData, radiusInt)
			if err == nil {
				imageData = rounded
				contentType = newContentType
			}
		}
	}

	transformCache.Set(cacheKey, CachedImage{ContentType: contentType, Data: imageData})

	if clientEtag == fmt.Sprintf(`"%s"`, cacheKey) {
		c.Status(http.StatusNotModified)
		return
	}

	maxAge := 86400
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d, must-revalidate", maxAge))
	c.Header("ETag", fmt.Sprintf(`"%s"`, cacheKey))
	if c.Request.Method == http.MethodHead {
		c.Status(200)
		return
	}
	c.Data(http.StatusOK, contentType, imageData)
}

func uploadPfpHandler(c *gin.Context) {
	var req UploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON data"})
		return
	}

	usersFile, err := os.ReadFile("users.json")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error reading users file"})
		return
	}

	var users []User
	if err := json.Unmarshal(usersFile, &users); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error parsing users file: " + err.Error()})
		return
	}

	var user *User
	for i := range users {
		if users[i].Key == req.Token {
			user = &users[i]
			break
		}
	}
	if user == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Invalid token"})
		return
	}

	if req.Image == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing image"})
		return
	}

	parts := strings.Split(req.Image, ",")
	if len(parts) != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid image format"})
		return
	}

	mimeHeader := parts[0]

	estimatedSize := (len(parts[1]) * 3) / 4
	if estimatedSize > 10*1024*1024 { // 10MB limit for decoded image
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image too large"})
		return
	}

	imageData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid image data"})
		return
	}

	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	os.MkdirAll(avatarDir, 0755)
	username := strings.ToLower(user.Username)

	tier := strings.ToLower(toString(user.GetSubscription()))
	isPro := slices.Contains([]string{"drive", "pro", "max"}, tier)

	var ext, contentType string
	switch {
	case strings.Contains(mimeHeader, "image/gif"):
		if isPro {
			ext = ".gif"
			contentType = "image/gif"
		} else {
			ext = ".jpg"
			contentType = "image/jpeg"
		}
	default:
		ext = ".jpg"
		contentType = "image/jpeg"
	}

	filePath := filepath.Join(avatarDir, username+ext)
	deleteAvatars(username)

	if contentType == "image/gif" {
		resizedData, err := resizeGIF(imageData, 256, 256)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error resizing GIF"})
			return
		}

		err = os.WriteFile(filePath, resizedData, 0644)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving GIF"})
			return
		}
	} else {
		img, _, err := image.Decode(bytes.NewReader(imageData))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
			return
		}

		resized := resize.Resize(256, 256, img, resize.Lanczos3)
		out, err := os.Create(filePath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving image"})
			return
		}
		defer out.Close()
		jpeg.Encode(out, resized, &jpeg.Options{Quality: 85})
	}

	transformCache.Clear()
	subscriptionCache = sync.Map{}

	c.JSON(http.StatusOK, gin.H{
		"status":  "Success",
		"message": "Profile picture uploaded successfully",
	})
}
