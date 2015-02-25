package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ushis/m3u"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	neturl "net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var TwitchUrl = regexp.MustCompile(`^https?://(?:www\.)?twitch\.tv/[^/]+/v/(\d+)`)

func must(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v", msg, err)
		os.Exit(1)
	}
}

type AccessResponse struct {
	Sig   string `json:"sig"`
	Token string `json:"token"`
}

func getAccessToken(videoId int) (sig, token string) {
	resp, err := http.Get(fmt.Sprintf("https://api.twitch.tv/api/vods/%d/access_token?as3=t", videoId))
	must(err, "Could not get access token")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Received status code %d while getting access token\n", resp.StatusCode)
		os.Exit(1)
	}
	respb, err := ioutil.ReadAll(resp.Body)
	must(err, "Could not read access token")
	var r AccessResponse
	must(json.Unmarshal(respb, &r), "Could not decode access token response")
	return r.Sig, r.Token
}

func dldPlaylist(url string) m3u.Playlist {
	uurl, err := neturl.Parse(url)
	must(err, fmt.Sprintf("Invalid url: %s", url))
	resp, err := http.Get(url)
	must(err, fmt.Sprintf("Could not get %s", url))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Received status code %d while getting %s\n", resp.StatusCode, url)
		os.Exit(1)
	}
	playlist, err := m3u.Parse(resp.Body)
	must(err, fmt.Sprintf("Could not read %s", url))
	for i := range playlist {
		u, err := uurl.Parse(playlist[i].Path)
		if err == nil {
			playlist[i].Path = u.String()
		}
	}
	return playlist
}

func getPlaylist(videoId int, quality string, sig, token string) m3u.Playlist {
	playlists := dldPlaylist(fmt.Sprintf("http://usher.twitch.tv/vod/%d?nauthsig=%s&nauth=%s", videoId, url.QueryEscape(sig), url.QueryEscape(token)))
	qualities := []string{}
	playlistUrl := ""

	for i := range playlists {
		v := strings.Split(playlists[i].Path, "/")
		q := v[7]
		qualities = append(qualities, q)

		if q == quality {
			playlistUrl = playlists[i].Path
		}
	}

	if playlistUrl == "" {
		fmt.Fprintf(os.Stderr, "Could not find requested quality: %v\n", qualities)
		os.Exit(1)
	}

	return dldPlaylist(playlistUrl)
}

func parseVideoId(arg string) int {
	videoId, err := strconv.Atoi(arg)
	if err == nil {
		return videoId
	}

	m := TwitchUrl.FindStringSubmatch(arg)

	if m != nil && len(m) == 2 {
		videoId, err = strconv.Atoi(m[1])
		if err == nil {
			return videoId
		}
	}

	fmt.Fprintf(os.Stderr, "Unrecognized url: %s (only twitch urls containing /v/ are supported)", arg)
	os.Exit(1)
	return -1
}

func setupOutput(videoId int, name string) io.WriteCloser {
	if name != "" {
		fh, err := os.Create(fmt.Sprintf("%s.ts", name))
		must(err, "Could not create output file")
		return fh
	} else {
		fh, err := os.Create(fmt.Sprintf("%d.ts", videoId))
		must(err, "Could not create output file")
		return fh
	}

}

func downloadStream(playlist m3u.Playlist, w io.Writer, startPosition int, endPosition int) {
	end := len(playlist) - 1
	if endPosition != -1 {
		end = endPosition
	}
	for i := startPosition; i <= end; i++ {
		fmt.Printf("\rDownloading part %d of %d and stopping at %d..", i, len(playlist), end)
		resp, err := http.Get(playlist[i].Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error while downloading: %v\n", err)
			return
		}
		_, err = io.Copy(w, resp.Body)
		resp.Body.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error while downloading: %v\n", err)
			return
		}
	}
	fmt.Printf("\nDone\n")
}

func main() {
	quality := flag.String("q", "high", "Selects video quality (defaults to 'high')")
	position := flag.Int("p", 0, "Selects starting position (defaults to 0)")
	end := flag.Int("e", -1, "Selects ending position (defaults to full vod)")
	name := flag.String("n", "", "Defines a name to save as (defaults to vod number)")
	flag.Parse()
	args := flag.Args()

	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Wrong number of arguments: twitchdown [flags] <twitch url or videoid>\n")
		flag.Usage()
		os.Exit(1)
	}

	videoId := parseVideoId(args[0])

	sig, token := getAccessToken(videoId)
	//fmt.Printf("sig=%s\ntoken=%s\n", sig, token)
	playlist := getPlaylist(videoId, *quality, sig, token)

	w := setupOutput(videoId, *name)
	defer w.Close()
	downloadStream(playlist, w, *position, *end)
}
