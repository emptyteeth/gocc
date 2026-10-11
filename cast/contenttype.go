package cast

import (
	"context"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// headTimeout bounds the HEAD request used to learn a stream's content
// type. The upstream library makes that request without any timeout, which
// hangs the command on a stream that never answers; this one does not.
const headTimeout = 3 * time.Second

// audioTypes maps the extensions of the audio a Chromecast plays to their
// content types. The device also plays video and images, but this app is
// for radio and music, and a wrong guess is worse than none.
var audioTypes = map[string]string{
	".mp3":  "audio/mpeg",
	".mp2":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".m4b":  "audio/mp4",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".webm": "audio/webm",
	".mp4":  "audio/mp4",
	".m3u8": "application/x-mpegurl",
	".pls":  "audio/x-scpls",
	".aacp": "audio/aacp",
}

// ContentType works out what a URL serves, to tell the device before it
// asks for it.
//
// The extension of the URL's path decides first, so that the common case
// costs no request at all. Only when the path says nothing is the URL
// asked with a HEAD request, with a short timeout: a stream that does not
// answer it is not a stream the device would play either, and the caller
// is not left waiting.
//
// An empty result means "unknown", which is a valid answer: the device
// then guesses from the URL itself.
func ContentType(ctx context.Context, url string) string {
	if ct := contentTypeFromPath(url); ct != "" {
		return ct
	}
	return contentTypeFromHead(ctx, url)
}

// contentTypeFromPath reads the content type from the URL's extension.
func contentTypeFromPath(url string) string {
	u := url
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return audioTypes[strings.ToLower(path.Ext(u))]
}

// contentTypeFromHead asks the URL what it serves, giving up quickly.
func contentTypeFromHead(ctx context.Context, url string) string {
	ctx, cancel := context.WithTimeout(ctx, headTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return ""
	}
	// Some stations refuse a request without a user agent.
	req.Header.Set("User-Agent", "gocc/1.0")
	client := &http.Client{Timeout: headTimeout}
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		res.Body.Close()
	}()

	ct := strings.TrimSpace(res.Header.Get("Content-Type"))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if !isPlayable(ct) {
		// A server that answers "text/html" for a stream is of no help;
		// the device does better guessing by itself.
		return ""
	}
	return ct
}

// isPlayable reports whether a content type is one a Chromecast plays, so
// that a web page's type is never passed on as if it were media.
func isPlayable(ct string) bool {
	switch {
	case strings.HasPrefix(ct, "audio/"),
		strings.HasPrefix(ct, "video/"),
		ct == "application/x-mpegurl",
		ct == "application/vnd.apple.mpegurl",
		ct == "application/ogg":
		return true
	}
	return false
}
