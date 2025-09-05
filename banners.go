package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

func loadDefaultBanner() {
	img := image.NewRGBA(image.Rect(0, 0, 3, 1))

	var buf bytes.Buffer
	png.Encode(&buf, img)
	defaultBannerContent = buf.Bytes()
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
