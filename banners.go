package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

func deleteBanners(username string) error {
	bannerDir := filepath.Join(documentPath, "rotur", "banners")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(bannerDir, base+ext)
		err := os.Remove(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func loadDefaultBanner() {
	img := image.NewRGBA(image.Rect(0, 0, 3, 1))

	var buf bytes.Buffer
	png.Encode(&buf, img)
	defaultBannerContent = buf.Bytes()
}

func getBannerPath(username string) (string, string, string, time.Time, error) {
	bannerPath := filepath.Join(documentPath, "rotur", "banners", username+".gif")
	fi, err := os.Stat(bannerPath)
	if err == nil {
		contentType := "image/gif"
		etag := fmt.Sprintf("%s-%d", username, time.Now().Unix())
		return bannerPath, contentType, etag, fi.ModTime(), nil
	}
	bannerPath = filepath.Join(documentPath, "rotur", "banners", username+".jpg")
	fi, err = os.Stat(bannerPath)
	if err == nil {
		contentType := "image/jpeg"
		etag := fmt.Sprintf("%s-%d", username, time.Now().Unix())
		return bannerPath, contentType, etag, fi.ModTime(), nil
	}

	return "", "", "", time.Time{}, os.ErrNotExist
}

func bannerHandler(c *gin.Context) {
	username, _ := strings.CutSuffix(strings.ToLower(c.Param("username")), ".gif")
	radius := c.Query("radius")
	radiusInt, parseErr := strconv.Atoi(strings.TrimSuffix(radius, "px"))
	needRounding := radius != "" && parseErr == nil && radiusInt > 0

	bannerPath, contentType, etag, modTime, err := getBannerPath(username)
	var imageData []byte
	if err != nil {
		imageData = defaultBannerContent
		contentType = "image/jpeg"
		needRounding = false
	}

	if !needRounding {
		c.Header("Content-Type", contentType)
		if etag != "" {
			c.Header("ETag", etag)
		}
		if !modTime.IsZero() {
			c.Header("Last-Modified", modTime.Format(http.TimeFormat))
		}
		if contentType == "image/gif" {
			c.Header("Cache-Control", "public, max-age=86400, must-revalidate")
		} else {
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		}
		if c.Request.Method == http.MethodHead {
			c.Status(200)
			return
		}
		if bannerPath != "" {
			c.File(bannerPath)
		} else {
			c.Data(http.StatusOK, contentType, imageData)
		}
		return
	}

	if c.Request.Method == http.MethodHead {
		c.Header("Content-Type", contentType)
		c.Status(200)
		return
	}

	// Load image data only if rounding is needed
	if bannerPath != "" {
		imageData, err = os.ReadFile(bannerPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error reading banner file"})
			return
		}
	}

	if contentType == "image/gif" {
		src, err := gif.DecodeAll(bytes.NewReader(imageData))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error decoding GIF"})
			return
		}
		rounded, err := roundGIF(src, radiusInt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error rounding GIF"})
			fmt.Println("Error rounding gif: " + err.Error())
			return
		}
		buf := bytes.NewBuffer(nil)
		err = gif.EncodeAll(buf, rounded)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error encoding GIF"})
			fmt.Println("Error encoding gif: " + err.Error())
			return
		}
		imageData = buf.Bytes()
		c.Header("Content-Type", "image/gif")
		c.Header("Cache-Control", "public, max-age=86400, must-revalidate")
		c.Data(http.StatusOK, "image/gif", imageData)
		return
	}

	// For non-GIF with rounding
	rounded, newContentType, err := roundCorners(imageData, radiusInt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error rounding image"})
		return
	}
	imageData = rounded
	contentType = newContentType
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Data(http.StatusOK, contentType, imageData)
}

func uploadBannerHandler(c *gin.Context) {
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
	mimeHeader := parts[0]

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

	tier := strings.ToLower(toString(user.GetSubscription()))
	isPro := strings.EqualFold(tier, "pro") || strings.EqualFold(tier, "max")

	var ext, contentType string
	switch {
	case strings.Contains(mimeHeader, "image/gif"):
		if isPro {
			ext = ".gif"
			contentType = "image/gif"
		} else {
			// downgrade to jpg if not pro
			ext = ".jpg"
			contentType = "image/jpeg"
		}
	case strings.Contains(mimeHeader, "image/png"):
		ext = ".png"
		contentType = "image/png"
	default:
		ext = ".jpg"
		contentType = "image/jpeg"
	}

	username := strings.ToLower(user.Username)
	bannerDir := filepath.Join(documentPath, "rotur", "banners")
	filePath := filepath.Join(bannerDir, username+ext)

	deleteBanners(username)

	if contentType == "image/gif" {
		// Pro users only
		resizedData, err := resizeGIF(imageData, 900, 300)
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
		resized := resize.Resize(900, 300, img, resize.Lanczos3)

		os.MkdirAll(bannerDir, 0755)

		filePath = filepath.Join(bannerDir, username+".jpg")
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
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "Success",
		"message": "Banner uploaded successfully",
	})
}
