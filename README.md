Downloads twitch.tv archived videos from the new HTTP Live Streaming system.
Usage:
```
	twitchdown [-q quality -p starting_position -e ending_position -n save_as] <twitch url or video id>
```
	
Note: only videos served from the HTTP Live Streaming system are supported, ie the url must contain /v/. Videos served from the old system can be downloaded with a plethora of other tools.

Use starting position to resume broken downloads and ending position to download a specific potion of the vod

Sorry if any unforgivable mistakes were made. I've zero previous experience in Go.

Created by: aarzilli 
Modified by: kareem-hewady