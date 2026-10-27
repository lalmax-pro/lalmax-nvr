# FFmpeg copy self-test fixture

`h264-aac.flv` is a 0.6-second synthetic black 320×240 H.264 video with a 48 kHz mono sine-wave AAC track. It contains no camera data and is embedded in the binary so copy-only profiles can be checked without requiring an H.264 encoder to generate their input. Generate it with:

```sh
ffmpeg -f lavfi -i 'color=c=black:s=320x240:r=15' \
  -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
  -t 0.6 -c:v libx264 -threads 1 -pix_fmt yuv420p -preset ultrafast -g 15 \
  -c:a aac -ac 1 -b:a 48k -f flv -y h264-aac.flv
```

This fixture checks a compatible example; runtime source codec checks and real protocol verification remain necessary.
