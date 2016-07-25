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

const DEBUG = false

var TwitchUrl = regexp.MustCompile(`^https?://(?:www\.|secure\.)?twitch\.tv/[^/]+/v/(\d+)`)
var PartUrl = regexp.MustCompile(`\?start_offset=(\d+)&end_offset=(\d+)$`)
var PartUrl2 = regexp.MustCompile(`-(\d+).ts$`)

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

type DownloadResult struct {
	Id   int
	Body io.ReadCloser
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
		var q string
		if len(v) == 6 {
			q = v[4]
		} else {
			q = v[7]
		}
		qualities = append(qualities, q)

		if q == quality {
			playlistUrl = playlists[i].Path
		}
	}

	if playlistUrl == "" && quality == "source" && len(playlists) > 0 {
		v := strings.Split(playlists[0].Path, "/")
		v[7] = "chunked"
		playlistUrl = strings.Join(v, "/")
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

func setupOutput(fileName string) io.WriteCloser {
	fh, err := os.Create(fileName)
	must(err, "Could not create output file")
	return fh
}

func downloadPart(url string, id int, c chan DownloadResult) {
	resp, err := http.Get(url)
	must(err, "Error while downloading")
	c <- DownloadResult{id, resp.Body}
}

func downloadStream(playlist m3u.Playlist, w io.Writer, startPosition int, endPosition int, threadCount int) {
	end := len(playlist) - 1
	if endPosition != -1 {
		end = endPosition
	}
	if threadCount > (end - startPosition + 1) {
		threadCount = end - startPosition + 1
	}
	c := make(chan DownloadResult)
	fmt.Printf("Downloading parts %d - %d of %d\n", startPosition, end, len(playlist))
	for i := startPosition; i <= startPosition+threadCount; i++ {
		go downloadPart(playlist[i].Path, i, c)
	}
	buffer := make([]io.ReadCloser, len(playlist))
	partToWrite := startPosition
	partToDownload := startPosition + threadCount
	for partToWrite <= end {
		res := <-c
		buffer[res.Id] = res.Body
		for partToWrite <= end && buffer[partToWrite] != nil {
			fmt.Printf("\rDownloaded part %d", partToWrite)
			_, err := io.Copy(w, buffer[partToWrite])
			buffer[partToWrite].Close()
			must(err, "Error while saving")
			partToWrite++
		}
		if partToDownload <= end {
			go downloadPart(playlist[partToDownload].Path, partToDownload, c)
			partToDownload++
		}
	}
	fmt.Printf("\nDone\n")
}

func continueDownload(fileName string, playlist m3u.Playlist) (int, io.WriteCloser) {
	fh, err := os.OpenFile(fileName, os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, setupOutput(fileName)
		}
		must(err, "Error opening output file")
	}

	fi, err := fh.Stat()
	must(err, "Can not stat output file")
	dldSz := fi.Size()

	ok, idx, out := continueDownloadOld(dldSz, playlist, fh)
	if ok {
		return idx, out
	}

	return continueDownloadNew(dldSz, playlist, fh)

}

func continueDownloadOld(dldSz int64, playlist m3u.Playlist, fh io.WriteCloser) (bool, int, io.WriteCloser) {
	acc := int64(0)

	for i := range playlist {
		var partSz int64

		m := PartUrl.FindStringSubmatch(playlist[i].Path)
		if m == nil || len(m) != 3 {
			return false, 0, nil
		}
		startOffset, err := strconv.Atoi(m[1])
		must(err, "Failed to parse part url, can not continue download")
		endOffset, err := strconv.Atoi(m[2])
		must(err, "Failed to parse part url, can not continue download")

		partSz = int64((endOffset - startOffset) + 1)

		if acc+partSz > dldSz {
			toSkip := dldSz - acc
			if DEBUG {
				fmt.Printf("Download continues from part %d, skipping %d bytes\n", i, toSkip)
			}

			resp, err := http.Get(playlist[i].Path)
			must(err, "Error while downloading")
			defer resp.Body.Close()
			bs, err := ioutil.ReadAll(resp.Body)
			must(err, "Error while downloading")

			_, err = fh.Write(bs[toSkip:])
			must(err, "Error while downloading")

			return true, i + 1, fh
		}
		acc += partSz
	}

	fmt.Fprintf(os.Stderr, "Nothing new to continue\n")
	os.Exit(0)
	return true, 0, nil
}

func continueDownloadNew(dldSz int64, playlist m3u.Playlist, fh io.WriteCloser) (int, io.WriteCloser) {
	lastStartOffset := int64(0)
	segmentOffset := int64(0)
	for i := range playlist {
		m := PartUrl2.FindStringSubmatch(playlist[i].Path)
		if m == nil || len(m) != 2 {
			fmt.Fprintf(os.Stderr, "could not continue download, could not parse part url")
			os.Exit(1)
			return 0, nil
		}

		z, err := strconv.Atoi(m[1])
		must(err, "Atoi")
		startOffset := int64(z)

		if startOffset == 0 && i != 0 {
			segmentOffset = lastStartOffset
			resp, err := http.Head(playlist[i-1].Path)
			must(err, "HEAD")
			segmentOffset += int64(resp.ContentLength)
		}

		startOffset += segmentOffset

		lastStartOffset = startOffset

		if int64(startOffset) > dldSz {
			toSkip := int64(startOffset) - dldSz

			if DEBUG {
				fmt.Printf("Download continues from part %d, skipping %d bytes\n", i-1, toSkip)
			}

			resp, err := http.Get(playlist[i-1].Path)
			must(err, "Error while downloading")
			defer resp.Body.Close()
			bs, err := ioutil.ReadAll(resp.Body)
			must(err, "Error while downloading")

			_, err = fh.Write(bs[toSkip:])
			must(err, "Error while downloading")

			return i, fh
		}
	}

	fmt.Fprintf(os.Stderr, "Nothing new to continue\n")
	os.Exit(0)
	return 0, nil
}

func main() {
	continueDld := flag.Bool("c", false, "Continues interrupted download")
	quality := flag.String("q", "high", "Selects video quality (defaults to 'high'). 'source' quality is not reported by twitch but still can be selected")
	position := flag.Int("p", 0, "Selects starting position (defaults to 0)")
	end := flag.Int("e", -1, "Selects ending position (defaults to full vod)")
	name := flag.String("n", "", "Defines a name to save as (defaults to vod number)")
	threadCount := flag.Int("t", 1, "Defines number of concurrent downloads. Does not work for continued downloads")
	flag.Parse()
	args := flag.Args()

	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Wrong number of arguments: twitchdown [flags] <twitch url or videoid>\n")
		flag.Usage()
		os.Exit(1)
	}

	videoId := parseVideoId(args[0])

	fileName := ""
	if *name != "" {
		fileName = fmt.Sprintf("%s.ts", *name)
	} else {
		fileName = fmt.Sprintf("%d.ts", videoId)
	}

	sig, token := getAccessToken(videoId)
	//fmt.Printf("sig=%s\ntoken=%s\n", sig, token)
	playlist := getPlaylist(videoId, *quality, sig, token)

	if DEBUG {
		fmt.Printf("PARTS:\n")
		for i := range playlist {
			fmt.Printf("%03d %s\n", i, playlist[i].Path)
		}
		fmt.Printf("\n")
	}

	var w io.WriteCloser
	if *continueDld && *position == 0 {
		*position, w = continueDownload(fileName, playlist)
	} else {
		w = setupOutput(fileName)
	}
	defer w.Close()
	downloadStream(playlist, w, *position, *end, *threadCount)
}
