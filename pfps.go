package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

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

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
		return
	}

	width := img.Bounds().Dx()

	size := width

	if width > 256 {
		user, err := getUserBy(username)
		if err == nil {
			maxSize, err := strconv.ParseInt(toString(user.MaxSize), 10, 64)
			if err == nil || maxSize <= 10_000_000 {
				size = 256
			}
		} else {
			size = 256
		}
	}

	if sizeStr != "" {
		size, err = strconv.Atoi(sizeStr)
		if err == nil && size > 0 {
			sizeEtag := fmt.Sprintf("%s-size-%d", etag, size)
			if clientEtag == fmt.Sprintf(`"%s"`, sizeEtag) {
				c.Status(http.StatusNotModified)
				return
			}

			if size > 256 {
				size = 256
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

			scale := float64(size) / 256

			ratio := int(math.Round(scale * float64(radiusInt)))

			rounded, newContentType, err := roundCorners(imageData, ratio)
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

	profileImageMax := 256

	rawSize := toString(user.MaxSize)
	if rawSize == "" {
		rawSize = "5000000"
	}

	maxSize, err := strconv.ParseInt(rawSize, 10, 64)
	// supporter tier on patreon (10 MB)
	if err == nil && maxSize > 10_000_000 {
		profileImageMax = 512
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

	resized := resize.Resize(uint(profileImageMax), uint(profileImageMax), img, resize.Lanczos3)

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
