# Spotify

Shows what's playing in the notch, with controls (play, pause, skip, seek, shuffle, repeat, volume, and which device it plays on). It can also pause your music while an agent waits for you and resume it afterwards.

Playback control needs **Spotify Premium**. It uses your own Spotify developer app, so nothing goes through anyone else's servers. Signing in uses PKCE, so there's no client secret to keep.

## Set up

1. **Install it.** In Hall Monitor, open **Settings → Extensions** and press **Install** next to Spotify.
2. **Create a Spotify app.** Go to [developer.spotify.com/dashboard](https://developer.spotify.com/dashboard), sign in, and press **Create app**:
   - **App name / description:** anything, for example "Hall Monitor".
   - **Redirect URI:** exactly `http://127.0.0.1:43821/callback`. Press **Add**.
   - **Which API/SDKs are you planning to use?** Tick **Web API**.
   - Accept the terms and **Save**.
3. **Copy the client ID.** On the app's page, under **Basic Information**, copy the **Client ID**: 32 letters and digits. You don't need the client secret.
4. **Connect.** Back in Settings → Extensions, paste it into **Client ID** and press **Connect**. Your browser opens Spotify's sign-in. Agree, and the tab says you're connected.

A new Spotify app is in development mode, which is fine here: it works for the account that created it. To use it with another Spotify account, add that account under **User Management** on the app's page.

## Switches

- **Show what's playing in the notch:** the player beside your agents when you hover the notch, and the cover and an equalizer around the notch while music plays.
- **Pause music while an agent waits:** when an agent needs your answer, the music pauses. Once none is waiting, it resumes after a moment, unless you've changed the music meanwhile.

## Privacy

- Your sign-in (a refresh token) is kept in the Keychain. The access token stays in the extension's memory.
- It asks Spotify only for `user-read-playback-state` and `user-modify-playback-state`: what's playing, and the controls.
- It runs in Hall Monitor's sandbox. It can reach the internet over HTTPS and listen on 127.0.0.1:43821 for the sign-in redirect. It can't read your files or start other programs.
- From your agents it gets only whether each one is working or waiting, not what it's doing.

## Troubleshooting

- **"INVALID_CLIENT: Invalid redirect URI"** in the browser: the redirect URI in your Spotify app must be exactly `http://127.0.0.1:43821/callback`, with `127.0.0.1` and not `localhost`.
- **"Couldn't listen on 127.0.0.1:43821"**: another program is using that port. Quit it and press Connect again.
- **"Spotify didn't allow that"**: controls need Premium, and some are off during ads.
- **Nothing shows:** start something in any Spotify app. Spotify only reports a player while one is open.
- **Its log** is in `~/Library/Application Support/HallMonitor/Extensions/spotify/log.txt`.

## Coming from the built-in Spotify

Earlier development builds had Spotify built in. When you install the extension, your client ID, switches and sign-in move over, so it starts connected.
