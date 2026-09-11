# camera-drive

A Raspberry Pi Zero 2 W that empties an Aiptek Cam 3200 by itself, and doubles
as a camcorder back-end for it.

The camera has 16MB of internal flash (15.3MB usable) and no memory card. That
is roughly **85 photos or 68 seconds of video** before it is full. Away from a
computer there is no way to clear it. This fixes that, and while we were in
there, lifts the video limit from 68 seconds to about **17.8 hours**.

## How you use it

Plug the camera into the Pi. That is the whole interface.

| Camera mode | USB id | What happens |
|---|---|---|
| Storage / Disk | `08ca:2023` | Everything is copied to the Pi, verified, and the LED blinks 3 times |
| Live / PC Camera | `08ca:2022` | Live viewfinder on your phone, with a shutter and a record button |

Then open the gallery on your phone to browse what it has, check free space,
and — only when you choose to — wipe the camera.

The camera reporting **two different USB product ids depending on its physical
mode switch** is what makes this work with no configuration at all. The Pi can
tell which mode you plugged in as and react accordingly.

## Getting to it

- On a known WiFi network: `http://cameradrive.local/` (or its IP)
- With no network around: the Pi raises its own AP, then `http://10.42.0.1/`

It switches between the two by itself, checking once a minute. It prefers being
a normal client; the AP is the fallback. See `camera-net-fallback`.

**Verified end to end on 2026-08-17**, by renaming the known SSIDs to something
nonexistent so the real fallback path had to run:

```
19:24:45  no known network reachable, starting access point
19:24:46  dnsmasq started, DHCP range 10.42.0.10 -- 10.42.0.254
19:25:38  DHCPACK(wlan0) 10.42.0.79 <phone>
          wlan0: inet 10.42.0.1/24
          curl http://10.42.0.1/ -> 200
```

AP comes up about 75 seconds after the known network vanishes.

### If the AP loads nothing on your phone

The Pi is almost certainly fine — check the phone first, in this order:

1. **Turn the VPN off.** A tunnel active across the network switch will route
   `10.42.0.1` into a path that no longer exists. This is the one that caught us.
2. **Mobile data.** A WiFi network with no internet makes phones quietly send
   everything over cellular. Tell it to stay connected, or turn data off.
3. Type `http://` explicitly so the browser does not try HTTPS or a search.

To confirm which side is at fault without a phone at all, run
`/usr/local/sbin/ap-diag.sh` on the Pi: it drops to AP mode, curls its own
address, dumps `ip addr`, the listening sockets and the firewall ruleset to
`/srv/camera-drive/apdiag.log`, and a separately armed timer restores the WiFi
afterwards. Always arm the revert timer BEFORE breaking the network.

## What is on the Pi

```
/usr/local/bin/camera-offload      copy + verify from the camera (root, udev-triggered)
/usr/local/bin/camera-wipe         the ONLY thing that deletes from the camera
/usr/local/bin/camera-live         owns the camera in live mode: preview + recording
/usr/local/bin/camera-drive-web    the phone-facing gallery (unprivileged)
/usr/local/bin/camera-net-fallback keeps the Pi reachable
/usr/local/sbin/netreport.sh       dumps network state to the boot partition each boot

/srv/camera-drive/media/           offloaded photos, in timestamped folders
/srv/camera-drive/media/snapshots/ photos taken from the phone
/run/camera-drive/live.jpg         current preview frame (tmpfs, never hits the SD card)
/run/camera-drive/control.json     shutter / record channel between the web app and camera-live
/srv/camera-drive/video/           camcorder recordings
/srv/camera-drive/index.json       sha256 -> stored path, the "do we already have this" record
/srv/camera-drive/status.json      last offload result, read by the web UI
```

Deploy changes with `platform/deploy.sh camera-drive` from the repo root. It
mirrors everything under `rootfs/` onto the Pi, preserving paths. The network
fallback and the provisioning scripts moved to `platform/`.

## The deletion rule

**A file is deleted from the camera only if its SHA-256 is already in the Pi's
index**, meaning a verified copy exists on the SD card. Anything else is kept
and reported. There is no override flag. If a copy is not safely on the Pi, the
answer is to offload again, not to force it.

Deletion is never automatic. Offloading happens the moment you plug in; wiping
takes a deliberate tap in the web UI, then a confirmation.

This was tested by planting a file the Pi had never seen and confirming it was
refused while the verified files were cleared.

## The UI

Three tabs: **Live**, **Photos**, **Tapes**.

- **Viewfinder toggle.** The sensor is idle until you ask for it. Plugging the
  camera in does not start it streaming — `camera-live` runs no ffmpeg at all
  until `preview` or `recording` is set, so a 20-year-old webcam is not pointed
  at a wall burning power for hours.
- **Shoot** saves the current frame. That frame is the camera's own JPEG, so a
  snapshot is a file copy at full native quality.
- **In-app viewer** with prev/next cycling, for both photos and video.
- **Select → Download .zip** for grabbing a batch. Submitted as a real form so
  the browser streams the download instead of buffering it in phone memory, and
  stored rather than deflated because JPEG does not compress twice.
- **Light / dark toggle**, remembered in localStorage, defaulting to the system
  preference.
- **Power button**, which runs a clean `systemctl poweroff`. Use it. Pulling
  power from a running Pi is the reliable way to corrupt an SD card eventually.

## Playing recordings in a browser

Browsers cannot decode MJPEG in Matroska, so a direct link to a recording used
to produce "no supported source". Two things fix that, and neither re-encodes:

- Direct links now send `Content-Disposition: attachment`, so they download
  cleanly instead of failing in a media player.
- In-app playback goes to `/api/play/<path>`, which runs
  `ffmpeg -re -i FILE -c:v copy -f image2pipe` and reframes the JPEGs it already
  contains as `multipart/x-mixed-replace`. An `<img>` renders it natively.
  `-re` paces it at the file's real speed. Measured at 3.3x realtime on a Zero
  2 W, versus roughly a quarter of realtime to transcode to H.264.

The default preview has **no audio**, because an `<img>` element has no audio
channel. That is inherent to the trick, not a bug — the sound is in the file.

For sound, the lightbox has a **♪ Sound** button, which fetches `/api/mp4/<path>`:
an H.264 + AAC copy transcoded on demand and cached, played in a real `<video>`
with a working seek bar. Contrary to what the live-capture measurements suggest,
this is cheap — the Pi's `h264_v4l2m2m` hardware encoder does **3.3x realtime**
from a file (4s for a 13.2s clip), so a one minute recording converts in about
18 seconds, once. It only failed in the capture pipeline because it was paired
with fragmented MP4 there; into a normal `+faststart` file it is fine.

One trap when writing that: ffmpeg picks its muxer from the output **file
extension**, so writing to a `.part` temp file fails instantly with "unable to
find a suitable output format". Pass `-f mp4` explicitly.

## Live view

Only one process can hold `/dev/video0` open, so a separate previewer and a
separate recorder cannot coexist. `camera-live` is the sole owner and fans one
capture out to two ffmpeg outputs: the Matroska recording (optional) and
`/run/camera-drive/live.jpg`, rewritten every frame. Both are `-c:v copy`, so
the preview costs nothing measurable and a snapshot is the camera's own JPEG
frame copied verbatim — no decode, no re-encode.

The browser gets it as `multipart/x-mixed-replace` from `/api/stream`, which an
`<img>` renders natively at about 12 fps with no JavaScript decoding. Frames not
ending in the JPEG end-of-image marker are skipped, because ffmpeg rewrites that
file in place many times a second and a reader will otherwise catch torn ones.

## Things that were not obvious, and cost time

**The camera's clock is dead.** EXIF timestamps read as 2001. Everything is
organised by ingest time. Never trust the camera's own dates.

**Filenames repeat.** The camera restarts at `IMG_0001` after every format, so
names cannot identify a photo. Content hashes do, which is also why re-plugging
the camera is a cheap no-op rather than a source of duplicates.

**cloud-init's NoCloud datasource reads `instance-id`, with a hyphen.** The
Raspberry Pi template ships `instance_id`, with an underscore, which is silently
ignored — cloud-init then falls back to its default of literally `nocloud`,
decides it has already provisioned this machine, and skips every
once-per-instance module including user creation. The symptom is a Pi that sets
its hostname (that module is frequency `always`) but has no user and no network.

**A Pi Zero 2 W has no real-time clock.** With no network it restores roughly
the image build date, so logs from a failed boot can look months old and appear
to be stale when they are actually from thirty seconds ago.

**ffmpeg cannot name this driver's pixel format.** `-input_format mjpeg` makes
the stream fail to open; `-input_format jpeg` is not a thing ffmpeg knows. Pass
neither and it negotiates 464x480 JPEG correctly on its own.

**Use `-channels`, not `-ac`, for ALSA input.** `-ac` is a codec option, never
reaches the demuxer, and ffmpeg then asks this mono-only microphone for stereo
and dies with `cannot set channel count to 2`.

**Do not re-encode the video.** Measured on this hardware:

| | frame rate |
|---|---|
| driver raw | 18.3 fps |
| through libx264 | 4.3 fps |
| remuxed MJPEG | 20.0 fps |

The camera emits JPEG already and records MJPEG/AVI natively. Copying the
stream through costs almost no CPU and loses nothing. The Pi's `h264_v4l2m2m`
hardware encoder produces structurally invalid MP4s here
(`missing picture in access unit`), with or without `dump_extra`.

**Matroska, not AVI.** AVI writes its index at close, so a recording that ends
by the power being pulled has no index, no duration, and a nonsense 600/1 frame
rate. Matroska stays seekable up to the truncation point — verified by cutting a
file in half and recovering 224 of 449 frames.

**`KillMode=mixed` is load-bearing.** systemd's default signals every process
in the unit, so ffmpeg dies at the same instant as its supervising script and
never finalises the file. The tell is an output file whose size is an exact
power-of-two boundary (23 MiB), and `ffprobe` reporting `File ended prematurely`.

**Do not restrict `CapabilityBoundingSet` on a service that calls sudo.**
Limiting it to `CAP_NET_BIND_SERVICE` also strips `CAP_SETUID`/`CAP_SETGID`,
and sudo fails with `unable to change to root gid`.

**`ProtectSystem=strict` makes everything read-only except `ReadWritePaths`.**
That includes `/run`. Listing only `/srv/camera-drive` left the control channel
unwritable and every shutter press failed with
`Read-only file system: /run/camera-drive/control.tmp`.

**But do not fix that by adding `/run/camera-drive` to `ReadWritePaths`.**
`/run` is tmpfs and is wiped on every boot, so the directory is absent at boot
and systemd cannot bind-mount a path that does not exist. The unit then fails
at `226/NAMESPACE` — "Failed to set up mount namespacing" — and restart-loops
forever. It looks fine until the first cold boot, because in a live session the
directory has usually already been created by something else. Use
`RuntimeDirectory=camera-drive` instead, which creates it before the unit starts
and adds it to the writable set, plus `RuntimeDirectoryPreserve=yes` so that
camera-live stopping on camera unplug does not delete the directory out from
under camera-drive-web.

**The `hidden` attribute loses to any author `display` rule.** `hidden` works
through a UA stylesheet rule of `display:none`, so `.lb{display:flex}` silently
overrides it and the element is permanently visible. For a `position:fixed;
inset:0` lightbox that means it covers the entire app and its close button
appears dead. Ship `[hidden]{display:none !important}` before any rule that sets
display. Note that checking `element.hidden` in a test will happily report the
correct state while the element is still painted over everything.

**The Zero 2 W's ACT LED trigger is `actpwr`, not an activity trigger.** Solid
on is normal and healthy. It is not a sign the board has hung.

**`-use_wallclock_as_timestamps` is not the fix for A/V drift here.** It sounds
right for a variable-rate source but produces non-monotonic DTS and a duration
2.5x too long. `-af aresample=async=1:first_pts=0` is what actually aligns the
two clocks without costing frames.

## Recovering a Pi that will not come up

`netreport.service` writes `/boot/firmware/netreport.txt` about 45 seconds into
every boot — IP, WiFi state, visible SSIDs, rfkill, regulatory domain, whether
sshd is listening, and the NetworkManager journal. That partition is vfat, so
pull the card, put it in any machine, and read the file. No mounting the root
filesystem, no sudo, no guessing.

`platform/provision/fix-pi-card.sh` re-applies the user, SSH key, WiFi profile and service symlinks
directly to the root partition from a laptop, bypassing cloud-init entirely.

## The thing that would make all of this unnecessary

A **2GB-or-smaller SD card**. The slot is labelled SD/MMC and takes a standard
full-size card; the camera predates SDHC and rejects anything above 2GB. A Sony
Memory Stick does not fit because it is 2.8mm thick against a 2.1mm slot.

The Pi is still the nicer workflow — it verifies every copy, keeps everything in
one place, and turns a 68-second camcorder into a 17-hour one.
