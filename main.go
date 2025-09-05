package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
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
	Username string `json:"username"`
	Key      string `json:"key"`
}

type UploadRequest struct {
	Image string `json:"image"`
	Token string `json:"token"`
}

func init() {
	loadDefaultImage()
	loadDefaultBanner()
}

func loadDefaultImage() {
	resp, err := http.Get(defaultImageURL)
	if err != nil {
		log.Printf("Error loading default image: %v", err)
		createFallbackImage()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		defaultImageContent, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading default image: %v", err)
			createFallbackImage()
			return
		}
		defaultImageEtag = fmt.Sprintf("%x", md5.Sum(defaultImageContent))
	} else {
		createFallbackImage()
	}
}

func createFallbackImage() {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 200, B: 200, A: 255})
		}
	}

	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	defaultImageContent = buf.Bytes()
	defaultImageEtag = fmt.Sprintf("%x", md5.Sum(defaultImageContent))
}

func loadDefaultBanner() {
	img := image.NewRGBA(image.Rect(0, 0, 3, 1))

	var buf bytes.Buffer
	png.Encode(&buf, img)
	defaultBannerContent = buf.Bytes()
}

func getAvatarImage(username string) ([]byte, string, time.Time, error) {
	filePath := filepath.Join(documentPath, "rotur", "avatars", strings.ToLower(username)+".jpg")

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, "", time.Time{}, err
	}

	etag := fmt.Sprintf("%s-%d", username, info.ModTime().Unix())

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "", time.Time{}, err
	}

	return data, etag, info.ModTime(), nil
}

func resizeImage(imageData []byte, size int) ([]byte, error) {
	cacheKey := fmt.Sprintf("%x-%d", md5.Sum(imageData), size)

	cacheMutex.RLock()
	if cached, exists := resizedCache[cacheKey]; exists {
		if time.Since(cached.Timestamp) < time.Duration(cacheTimeout)*time.Second {
			cacheMutex.RUnlock()
			return cached.Data, nil
		}
	}
	cacheMutex.RUnlock()

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return imageData, err
	}

	resized := resize.Resize(uint(size), uint(size), img, resize.Lanczos3)

	var buf bytes.Buffer
	err = jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 85})
	if err != nil {
		return imageData, err
	}

	result := buf.Bytes()

	cacheMutex.Lock()
	resizedCache[cacheKey] = CachedImage{
		Data:        result,
		ContentType: "image/jpeg",
		Timestamp:   time.Now(),
	}
	cacheMutex.Unlock()

	return result, nil
}

func roundCorners(imageData []byte, radius int) ([]byte, string, error) {
	cacheKey := fmt.Sprintf("%x-%d", md5.Sum(imageData), radius)

	cacheMutex.RLock()
	if cached, exists := roundedCache[cacheKey]; exists {
		if time.Since(cached.Timestamp) < time.Duration(cacheTimeout)*time.Second {
			cacheMutex.RUnlock()
			return cached.Data, cached.ContentType, nil
		}
	}
	cacheMutex.RUnlock()

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return imageData, "image/jpeg", err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if radius > height/2 {
		radius = height / 2
	}

	result := image.NewRGBA(bounds)

	isInRoundedRect := func(x, y int) bool {
		topLeftCenterX := radius
		topLeftCenterY := radius
		topRightCenterX := width - radius - 1
		topRightCenterY := radius
		bottomLeftCenterX := radius
		bottomLeftCenterY := height - radius - 1
		bottomRightCenterX := width - radius - 1
		bottomRightCenterY := height - radius - 1

		if x < radius && y < radius {
			dx := x - topLeftCenterX
			dy := y - topLeftCenterY
			return dx*dx+dy*dy <= radius*radius
		} else if x >= width-radius && y < radius {
			dx := x - topRightCenterX
			dy := y - topRightCenterY
			return dx*dx+dy*dy <= radius*radius
		} else if x < radius && y >= height-radius {
			dx := x - bottomLeftCenterX
			dy := y - bottomLeftCenterY
			return dx*dx+dy*dy <= radius*radius
		} else if x >= width-radius && y >= height-radius {
			dx := x - bottomRightCenterX
			dy := y - bottomRightCenterY
			return dx*dx+dy*dy <= radius*radius
		}

		return true
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if isInRoundedRect(x-bounds.Min.X, y-bounds.Min.Y) {
				result.Set(x, y, img.At(x, y))
			} else {
				result.Set(x, y, color.RGBA{0, 0, 0, 0})
			}
		}
	}

	var buf bytes.Buffer
	err = png.Encode(&buf, result)
	if err != nil {
		return imageData, "image/jpeg", err
	}

	resultData := buf.Bytes()

	cacheMutex.Lock()
	roundedCache[cacheKey] = CachedImage{
		Data:        resultData,
		ContentType: "image/png",
		Timestamp:   time.Now(),
	}
	cacheMutex.Unlock()

	return resultData, "image/png", nil
}

func enableCORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowMethods = []string{"GET", "POST", "OPTIONS"}
	config.AllowHeaders = []string{"Content-Type"}
	return cors.New(config)
}

func avatarHandler(c *gin.Context) {
	username := strings.ToLower(c.Param("username"))
	radius := c.Query("radius")
	sizeStr := c.Query("s")

	clientEtag := c.GetHeader("If-None-Match")

	imageData, etag, _, err := getAvatarImage(username)
	if err != nil {
		imageData = defaultImageContent
		etag = defaultImageEtag
	}

	contentType := "image/jpeg"
	finalEtag := etag

	if sizeStr != "" {
		size, err := strconv.Atoi(sizeStr)
		if err == nil && size > 0 && size <= 256 && size != 256 {
			sizeEtag := fmt.Sprintf("%s-size-%d", etag, size)
			if clientEtag == fmt.Sprintf(`"%s"`, sizeEtag) {
				c.Status(http.StatusNotModified)
				return
			}

			resized, err := resizeImage(imageData, size)
			if err == nil {
				imageData = resized
				finalEtag = sizeEtag
			}
		}
	}

	if radius != "" {
		radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
		if err == nil && radiusInt > 0 {
			radiusEtag := fmt.Sprintf("%s-rounded-%s", finalEtag, radius)
			if clientEtag == fmt.Sprintf(`"%s"`, radiusEtag) {
				c.Status(http.StatusNotModified)
				return
			}

			rounded, newContentType, err := roundCorners(imageData, radiusInt)
			if err == nil {
				imageData = rounded
				contentType = newContentType
				finalEtag = radiusEtag
			}
		}
	}

	if clientEtag == fmt.Sprintf(`"%s"`, finalEtag) {
		c.Status(http.StatusNotModified)
		return
	}

	maxAge := 86400
	if strings.Contains(finalEtag, "rounded") {
		maxAge = 604800
	}

	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d, must-revalidate", maxAge))
	c.Header("ETag", fmt.Sprintf(`"%s"`, finalEtag))
	c.Data(http.StatusOK, contentType, imageData)
}

func bannerHandler(c *gin.Context) {
	username := strings.ToLower(c.Param("username"))
	radius := c.Query("radius")

	filePath := filepath.Join(documentPath, "rotur", "banners", username+".jpg")

	var imageData []byte
	var contentType string

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		imageData = defaultBannerContent
		contentType = "image/png"
	} else {
		data, err := os.ReadFile(filePath)
		if err != nil {
			imageData = defaultBannerContent
			contentType = "image/png"
		} else {
			imageData = data
			contentType = "image/jpeg"
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

	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error parsing users file"})
		return
	}

	var user *User
	for _, u := range users {
		if u.Key == req.Token {
			user = &u
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

	imageData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid image format"})
		return
	}

	if len(imageData) > 5*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image size exceeds 5MB limit"})
		return
	}

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
		return
	}

	resized := resize.Resize(256, 256, img, resize.Lanczos3)

	username := strings.ToLower(user.Username)
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	os.MkdirAll(avatarDir, 0755)

	filePath := filepath.Join(avatarDir, username+".jpg")
	file, err := os.Create(filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving image"})
		return
	}
	defer file.Close()

	err = jpeg.Encode(file, resized, &jpeg.Options{Quality: 85})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error encoding image"})
		return
	}

	cacheMutex.Lock()
	resizedCache = make(map[string]CachedImage)
	roundedCache = make(map[string]CachedImage)
	cacheMutex.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"status":  "Success",
		"message": "Profile picture uploaded successfully",
	})
}

func uploadBannerHandler(c *gin.Context) {
	clientIP := c.ClientIP()
	if clientIP != "127.0.0.1" && clientIP != "::1" && clientIP != "localhost" {
		c.JSON(http.StatusForbidden, gin.H{"error": "This endpoint can only be accessed locally"})
		return
	}

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error parsing users file"})
		return
	}

	var user *User
	for _, u := range users {
		if u.Key == req.Token {
			user = &u
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

	imageData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid image format"})
		return
	}

	if len(imageData) > 10*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image size exceeds 10MB limit"})
		return
	}

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
		return
	}

	resized := resize.Resize(1200, 400, img, resize.Lanczos3)

	username := strings.ToLower(user.Username)
	bannerDir := filepath.Join(documentPath, "rotur", "banners")
	os.MkdirAll(bannerDir, 0755)

	filePath := filepath.Join(bannerDir, username+".jpg")
	file, err := os.Create(filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error saving banner"})
		return
	}
	defer file.Close()

	err = jpeg.Encode(file, resized, &jpeg.Options{Quality: 85})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error encoding banner"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "Success",
		"message": "Banner uploaded successfully",
	})
}

func main() {
	gin.SetMode(gin.ReleaseMode)

	r := gin.Default()

	r.Use(enableCORS())

	r.GET("/:username", avatarHandler)
	r.GET("/.banners/:username", bannerHandler)
	r.POST("/rotur-upload-pfp", uploadPfpHandler)
	r.POST("/rotur-upload-banner", uploadBannerHandler)

	log.Printf("Avatar service starting on port %s", port)
	r.Run(":" + port)
}
