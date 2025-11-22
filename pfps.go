package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nfnt/resize"
)

func deleteAvatars(username string) error {
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(avatarDir, base+ext)
		err := os.Remove(filePath)
		if err != nil {
			return err
		}
	}

	return nil
}

func getAvatarImage(username string) ([]byte, string, string, time.Time, error) {
	avatarDir := filepath.Join(documentPath, "rotur", "avatars")
	base := strings.ToLower(username)

	extensions := []string{".gif", ".jpg"}
	for _, ext := range extensions {
		filePath := filepath.Join(avatarDir, base+ext)
		info, err := os.Stat(filePath)
		if err == nil {
			data, err := os.ReadFile(filePath)
			if err != nil {
				return nil, "", "", time.Time{}, err
			}
			contentType := "image/jpeg"
			switch ext {
			case ".png":
				contentType = "image/png"
			case ".gif":
				contentType = "image/gif"
			}
			etag := fmt.Sprintf("%s-%d", username, info.ModTime().Unix())
			return data, contentType, etag, info.ModTime(), nil
		}
	}

	return nil, "", "", time.Time{}, os.ErrNotExist
}

func avatarHandler(c *gin.Context) {
	username, _ := strings.CutSuffix(strings.ToLower(c.Param("username")), ".gif")
	radius := c.Query("radius")
	sizeStr := c.Query("s")

	clientEtag := c.GetHeader("If-None-Match")

	imageData, contentType, etag, _, err := getAvatarImage(username)
	if err != nil {
		imageData = defaultImageContent
		contentType = "image/jpeg"
		etag = defaultImageEtag
	}

	finalEtag := etag

	// --- Handle GIFs ---
	if contentType == "image/gif" {

		if sizeStr == "" && radius == "" {
			c.File(filepath.Join(documentPath, "rotur", "avatars", username+".gif"))
			return
		}
		// Handle size
		if sizeStr != "" {
			sz, err := strconv.Atoi(sizeStr)
			if err == nil && sz > 0 && sz <= 256 {
				resizedData, err := resizeGIF(imageData, sz, sz)
				if err == nil {
					imageData = resizedData
					finalEtag += fmt.Sprintf("-size-%d", sz)
				}
			}
		}

		if radius != "" {
			radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
			if err == nil && radiusInt > 0 {
				src, err := gif.DecodeAll(bytes.NewReader(imageData))
				if err == nil {
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
					finalEtag += "-rounded"
				}
			}
		}

		if clientEtag == fmt.Sprintf(`"%s"`, finalEtag) {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("Content-Type", "image/gif")
		c.Header("Cache-Control", "public, max-age=86400, must-revalidate")
		c.Header("ETag", fmt.Sprintf(`"%s"`, finalEtag))
		c.Data(http.StatusOK, "image/gif", imageData)
		return
	}
	if sizeStr == "" && radius == "" && etag != defaultImageEtag {
		c.File(filepath.Join(documentPath, "rotur", "avatars", username+".jpg"))
		return
	}

	// --- Static formats (jpg/png) ---
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Error decoding image"})
		return
	}

	if sizeStr != "" {
		sz, err := strconv.Atoi(sizeStr)
		if err == nil && sz > 0 && sz <= 256 {
			resized := resize.Resize(uint(sz), 0, img, resize.Lanczos3)
			var buf bytes.Buffer
			jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 85})
			imageData = buf.Bytes()
			finalEtag = fmt.Sprintf("%s-size-%d", etag, sz)
		}
	}

	if radius != "" {
		radiusInt, err := strconv.Atoi(strings.TrimSuffix(radius, "px"))
		if err == nil && radiusInt > 0 {
			rounded, newContentType, err := roundCorners(imageData, radiusInt)
			if err == nil {
				imageData = rounded
				contentType = newContentType
				finalEtag += "-rounded"
			}
		}
	}

	if clientEtag == fmt.Sprintf(`"%s"`, finalEtag) {
		c.Status(http.StatusNotModified)
		return
	}

	maxAge := 86400
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
			// downgrade to jpg if not pro
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
		// Pro users only
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

	cacheMutex.Lock()
	resizedCache = make(map[string]CachedImage)
	roundedCache = make(map[string]CachedImage)
	cacheMutex.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"status":  "Success",
		"message": "Profile picture uploaded successfully",
	})
}
