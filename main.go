package main

import (
	"embed"
	"github.com/general252/rtsp_to_webrtc/pkg"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
)

//go:embed static/*
var fileContent embed.FS

func main() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)

	// Create a new API using our SettingEngine
	engine := pkg.NewWebRtcEngine(2000)
	_ = engine

	r := gin.Default()

	// 从定向
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/static/index.html")
	})
	// 静态文件
	r.GET("/static/*filepath", func(c *gin.Context) {
		http.FileServer(http.FS(fileContent)).ServeHTTP(c.Writer, c.Request)
	})
	r.POST("/RtspToWebrtc", engine.RtspToWebrtc)
	r.POST("/WebrtcToRtsp", engine.WebrtcToRtsp)
	r.POST("/GetWebrtc", engine.GetWebrtc)

	log.Println("Open http://127.0.0.1:8080 to access this demo")
	if err := r.Run(":8080"); err != nil {
		log.Println(err)
	}
}
