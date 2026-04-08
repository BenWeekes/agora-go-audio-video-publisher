# go-audio-video-publisher

Standalone test app that publishes audio and video from an MP4 file into an Agora channel.

This lives outside `agent-uikit` on purpose. It is based on the local Agora Go Server SDK `send_mp4` example, but packaged as a separate project with cleaner flags and local wiring to the checked-out SDK.

## What it does

- joins an Agora RTC channel as a broadcaster
- decodes MP4 audio to PCM and publishes it
- decodes MP4 video to I420/YUV and publishes it
- can run in `audio-only` or `video-only` mode for debugging

## Requirements

- the input file is an `.mp4` with H.264 video
- FFmpeg development libraries are installed locally
- the Agora native SDK assets are downloaded under `../server-custom-llm/go-audio-subscriber/sdk`
- on macOS, the runtime loader needs `DYLD_LIBRARY_PATH` pointing at `../server-custom-llm/go-audio-subscriber/sdk/agora_sdk_mac`

## Setup

1. Ensure the native Agora SDK assets are present:

```bash
cd /Users/benweekes/work/codex/server-custom-llm/go-audio-subscriber/sdk
make deps
```

2. Ensure FFmpeg and pkg-config are installed and visible in the shell.

3. Build the publisher:

```bash
cd /Users/benweekes/work/codex/go-audio-video-publisher
go build .
```

## Usage

Run with an explicit token:

```bash
export AGORA_APP_ID=your-app-id
export AGORA_TOKEN=your-rtc-token
export DYLD_LIBRARY_PATH=/Users/benweekes/work/codex/server-custom-llm/go-audio-subscriber/sdk/agora_sdk_mac

go run . \
  --channel your-channel \
  --uid 73 \
  --input /absolute/path/to/input.mp4
```

Or let the demo generate the RTC token locally from the App Certificate:

```bash
export AGORA_APP_ID=your-app-id
export AGORA_APP_CERTIFICATE=your-app-certificate
export DYLD_LIBRARY_PATH=/Users/benweekes/work/codex/server-custom-llm/go-audio-subscriber/sdk/agora_sdk_mac

go run . \
  --channel your-channel \
  --uid 73 \
  --input /absolute/path/to/input.mp4
```

Example from the verified test run:

```bash
env \
  AGORA_APP_ID=***** \
  AGORA_APP_CERTIFICATE=***** \
  DYLD_LIBRARY_PATH=/Users/benweekes/work/codex/server-custom-llm/go-audio-subscriber/sdk/agora_sdk_mac \
  ./go-audio-video-publisher \
  --channel your-channel-id \
  --uid your-uid \
  --input /absolute/path/to/input.mp4
```

Optional flags:

- `--audio-only`
- `--video-only`
- `--uid some-string-user-id`
- `--debug-sleep-log`

## Token generation

If `--token` or `AGORA_TOKEN` is not supplied, the app generates an RTC token locally using:

- package: `github.com/AgoraIO/Tools/DynamicKey/AgoraDynamicKey/go/src/rtctokenbuilder2`
- function: `BuildTokenWithUserAccount`

That is the standard Agora DynamicKey token builder package used by the local Go Server SDK examples as well.

## Notes

- The working publish path in this demo is decoded PCM audio plus decoded I420 video.
- Audio is re-chunked into 10 ms PCM blocks because the Agora SDK requires that framing for `PushAudioPcmData`.
- Agora SDK logs are written under `./agora_rtc_log/`.
