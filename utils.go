package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/esimov/colorquant"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/logica0419/resigif"
	"github.com/nfnt/resize"
)

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

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if isPixelInRoundedRect(x-bounds.Min.X, y-bounds.Min.Y, width, height, radius) {
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

func roundGIF(src *gif.GIF, radius int) (*gif.GIF, error) {
	if len(src.Image) == 0 {
		return nil, fmt.Errorf("no frames in GIF")
	}

	bounds := image.Rect(0, 0, src.Config.Width, src.Config.Height)
	width, height := bounds.Dx(), bounds.Dy()

	if radius > width/2 {
		radius = width / 2
	}
	if radius > height/2 {
		radius = height / 2
	}

	if radius <= 0 {
		return src, nil // No rounding
	}

	dst := &gif.GIF{
		LoopCount: src.LoopCount,
		Delay:     src.Delay,
		Disposal:  make([]byte, len(src.Disposal)),
		Image:     make([]*image.Paletted, len(src.Image)),
		Config:    src.Config,
	}

	var bgColor color.Color
	if src.BackgroundIndex < byte(len(src.Image[0].Palette)) {
		bgColor = src.Image[0].Palette[src.BackgroundIndex]
	} else {
		bgColor = color.Transparent
	}

	compositor := image.NewRGBA(bounds)
	draw.Draw(compositor, bounds, &image.Uniform{bgColor}, image.Point{}, draw.Src)

	var prev *image.RGBA
	var frameRect image.Rectangle

	for i := range src.Image {
		frame := src.Image[i]
		frameRect = frame.Bounds()

		if src.Disposal[i] == gif.DisposalPrevious {
			prev = image.NewRGBA(bounds)
			draw.Draw(prev, bounds, compositor, image.Point{}, draw.Src)
		}

		draw.Draw(compositor, frameRect, frame, frameRect.Min, draw.Over)

		// Copy for output and apply mask
		inputRGBA := image.NewRGBA(bounds)
		draw.Draw(inputRGBA, bounds, compositor, image.Point{}, draw.Src)

		// Quantize to paletted with dithering
		paletted := image.NewPaletted(bounds, palette.WebSafe)
		ditherer := colorquant.Dither{
			Filter: [][]float32{
				{0.0, 0.0, 7.0 / 16.0},
				{3.0 / 16.0, 5.0 / 16.0, 1.0 / 16.0},
			},
		}
		_, ok := uniqueColors(inputRGBA, 255)
		var outputRGBA *image.RGBA
		if !ok {
			outputRGBA = toRGBA(ditherer.Quantize(inputRGBA, paletted, 255, true, false))
		} else {
			// Collect unique colors since no quantization needed
			unique := make(map[color.Color]struct{})
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					unique[inputRGBA.At(x, y)] = struct{}{}
				}
			}

			colorIndex := make(map[color.Color]uint8)
			var pal color.Palette
			idx := uint8(0)
			for col := range unique {
				pal = append(pal, col)
				colorIndex[col] = idx
				idx++
			}
			paletted.Palette = pal
			stride := paletted.Stride
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					paletted.Pix[y*stride+x] = colorIndex[inputRGBA.At(x, y)]
				}
			}
			outputRGBA = toRGBA(paletted)
		}

		pix := outputRGBA.Pix
		stride := outputRGBA.Stride
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				if !isPixelInRoundedRect(x, y, width, height, radius) {
					pix[(y*stride+x*4)+3] = 0
				}
			}
		}

		// Add transparent color if not present
		hasTrans := false
		for _, c := range paletted.Palette {
			_, _, _, a := c.RGBA()
			if a == 0 {
				hasTrans = true
				break
			}
		}
		if !hasTrans {
			if len(paletted.Palette) >= 256 {
				return nil, fmt.Errorf("no room for transparent color after quantization")
			}
			paletted.Palette = append(paletted.Palette, color.Transparent)
		}

		// Find transparent index
		var transIndex uint8
		for idx, c := range paletted.Palette {
			_, _, _, a := c.RGBA()
			if a == 0 {
				transIndex = uint8(idx)
				break
			}
		}

		// Override transparent areas with transparent index
		stride = paletted.Stride
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				_, _, _, a := outputRGBA.At(x, y).RGBA()
				if a == 0 {
					paletted.Pix[y*stride+x] = transIndex
				}
			}
		}

		dst.Image[i] = paletted
		dst.Disposal[i] = gif.DisposalNone

		// Apply disposal to compositor (unmasked)
		switch src.Disposal[i] {
		case gif.DisposalBackground:
			draw.Draw(compositor, frameRect, &image.Uniform{bgColor}, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if prev != nil {
				draw.Draw(compositor, bounds, prev, image.Point{}, draw.Src)
			}
			// DisposalNone: leave as is
		}
	}

	return dst, nil
}

func toRGBA(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, src, bounds.Min, draw.Src)
	return rgba
}

func uniqueColors(img image.Image, limit int) (int, bool) {
	seen := make(map[color.Color]struct{})
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.At(x, y)
			if _, ok := seen[c]; !ok {
				seen[c] = struct{}{}
				if len(seen) > limit {
					return len(seen), false // too many colors
				}
			}
		}
	}
	return len(seen), true // safe to skip quantization
}

func isPixelInRoundedRect(x, y, width, height, radius int) bool {
	corners := []struct{ cx, cy int }{
		{radius, radius},                          // top-left
		{width - radius - 1, radius},              // top-right
		{radius, height - radius - 1},             // bottom-left
		{width - radius - 1, height - radius - 1}, // bottom-right
	}

	switch {
	case x < radius && y < radius:
		dx, dy := x-corners[0].cx, y-corners[0].cy
		return dx*dx+dy*dy <= radius*radius
	case x >= width-radius && y < radius:
		dx, dy := x-corners[1].cx, y-corners[1].cy
		return dx*dx+dy*dy <= radius*radius
	case x < radius && y >= height-radius:
		dx, dy := x-corners[2].cx, y-corners[2].cy
		return dx*dx+dy*dy <= radius*radius
	case x >= width-radius && y >= height-radius:
		dx, dy := x-corners[3].cx, y-corners[3].cy
		return dx*dx+dy*dy <= radius*radius
	default:
		return true
	}
}

func resizeGIF(data []byte, width, height int) ([]byte, error) {
	src, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	dstImg, err := resigif.Resize(ctx, src, width, height, resigif.WithAspectRatio(resigif.Ignore))
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	err = gif.EncodeAll(buf, dstImg)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

func enableCORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowMethods = []string{"GET", "POST", "OPTIONS"}
	config.AllowHeaders = []string{"Content-Type"}
	return cors.New(config)
}

func toString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case []any:
		var s string
		for _, item := range v {
			s += toString(item)
		}
		return s
	case map[string]any:
		var s string
		for k, v := range v {
			s += fmt.Sprintf("%s: %s\n", k, toString(v))
		}
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

func mustEnv(key string, def string) string {
	val := os.Getenv(key)
	if val == "" {
		if def != "" {
			return def
		}
		log.Printf("[config] WARNING: %s not set", key)
	}
	return val
}

var ADMIN_TOKEN string
var envOnce sync.Once

func loadEnvFile() {
	// Prefer workspace root .env (one directory up) then fall back to local .env.
	// Root file now holds authoritative configuration; local .env may override selectively if present.
	parent := "../.env"
	local := ".env"
	if _, err := os.Stat(parent); err == nil {
		if err := godotenv.Overload(parent); err != nil {
			log.Printf("[env] failed to load parent .env: %v", err)
		} else {
			log.Printf("[env] loaded parent .env (%s)", parent)
		}
	} else {
		log.Printf("[env] parent .env not found (%s): %v", parent, err)
	}
	if _, err := os.Stat(local); err == nil {
		if err := godotenv.Overload(local); err != nil {
			log.Printf("[env] failed to load local .env overrides: %v", err)
		} else {
			log.Printf("[env] loaded local .env overrides (%s)", local)
		}
	}
	// Reload config variables after populating environment
	ADMIN_TOKEN = mustEnv("ADMIN_TOKEN", "")
}

func getStringOrDefault(val any, defaultVal string) string {
	if val == nil {
		return defaultVal
	}
	if s, ok := val.(string); ok {
		return s
	}
	return defaultVal
}
