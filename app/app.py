import ast
import logging
import os
import random

import requests
from flask import Flask, redirect, render_template, request, session, url_for, jsonify
from flask_bootstrap import Bootstrap5
from flask_wtf import CSRFProtect, FlaskForm
from werkzeug.middleware.proxy_fix import ProxyFix
from wtforms import StringField, SubmitField
from wtforms.validators import DataRequired

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = Flask(__name__)


app.wsgi_app = ProxyFix(app.wsgi_app, x_for=1, x_proto=1, x_host=1, x_prefix=1)

app.secret_key = os.environ.get("SECRET_KEY", "dev-secret-key-please-change")
Bootstrap5(app)
csrf = CSRFProtect(app)

SCOPE = (
    "playlist-modify-private playlist-modify-public user-read-private "
    "user-read-email user-read-currently-playing user-read-playback-state"
)
def get_secret(secret_name, env_name, default=""):
    try:
        with open(f"/run/secrets/{secret_name}", "r") as f:
            return f.read().strip()
    except IOError:
        return os.environ.get(env_name, default)

CLIENT_ID = get_secret("CLIENT_ID", "CLIENT_ID")
CLIENT_SECRET = get_secret("CLIENT_SECRET", "CLIENT_SECRET")

if not CLIENT_ID or not CLIENT_SECRET:
    logger.warning("Spotify CLIENT_ID and CLIENT_SECRET are not set in the environment.")


class SongForm(FlaskForm):
    song = StringField('Song', validators=[DataRequired()])
    submit = SubmitField('Search')


class PlaylistForm(FlaskForm):
    playlist = StringField('Playlist', validators=[DataRequired()])
    submit = SubmitField('Done')


def get_auth_header():
    token = session.get('token_data')
    return {'Authorization': f'Bearer {token}'} if token else {}


@app.route('/health')
def health_check():
    return jsonify(status="healthy"), 200


@app.route('/', methods=['GET', 'POST'])
def main_page():
    form = SongForm()
    ready = session.get('ready', False)
    songs = []

    if ready and form.validate_on_submit():
        song_query = form.song.data
        try:
            response = requests.get(
                'https://api.spotify.com/v1/search',
                headers=get_auth_header(),
                params={'q': song_query, 'type': 'track'},
                timeout=10
            )
            response.raise_for_status()
            data = response.json()

            items = data.get("tracks", {}).get("items", [])
            songs = [
                {
                    "URI": item.get("uri"),
                    "name": item.get("name"),
                    "artists": [artist.get("name") for artist in item.get("artists", [])],
                    "img": item.get("album", {}).get("images", [{}])[0].get("url") if item.get("album", {}).get(
                        "images") else ""
                }
                for item in items
            ]
            return render_template("song.html", list_of_songs=songs, ready=ready)
        except requests.RequestException as e:
            logger.error(f"Error searching for song: {e}")

    added_songs = session.get('added_songs', [])
    return render_template("index.html", form=form, added_songs=added_songs, ready=ready)


@app.route('/adding_to_playlist')
def adding_to_playlist():
    list_of_songs_to_add = session.get('added_songs', [])
    playlist_id = session.get('playlist_id')
    index = session.get('index', 0)

    if not playlist_id or not list_of_songs_to_add:
        return redirect(url_for('main_page'))

    headers = get_auth_header()
    headers['Content-Type'] = 'application/json'

    try:
        for song in list_of_songs_to_add:
            position = 0 if index == 0 else random.randint(0, index)
            payload = {'uris': [song['URI']], 'position': position}

            response = requests.post(
                f'https://api.spotify.com/v1/playlists/{playlist_id}/tracks',
                headers=headers,
                json=payload,
                timeout=10
            )
            response.raise_for_status()
            index += 1

        session['index'] = index
        session['added_songs'] = []
    except requests.RequestException as e:
        logger.error(f"Error adding to playlist: {e}")

    return redirect(url_for('main_page'))


@app.route('/adding_to_list/')
def adding_to_list():
    song_data_str = request.args.get('song')
    if not song_data_str:
        return redirect(url_for('main_page'))

    try:
        song_data = ast.literal_eval(song_data_str)
        added_songs = session.get('added_songs', [])

        if not any(s.get('URI') == song_data.get('URI') for s in added_songs):
            added_songs.append(song_data)
            session['added_songs'] = added_songs
    except (ValueError, SyntaxError) as e:
        logger.error(f"Error parsing song data: {e}")

    return redirect(url_for('main_page'))


@app.route('/removing_from_list')
def removing_from_list():
    song_data_str = request.args.get('song')
    if not song_data_str:
        return redirect(url_for('main_page'))

    try:
        song_data = ast.literal_eval(song_data_str)
        added_songs = session.get('added_songs', [])
        added_songs = [s for s in added_songs if s.get('URI') != song_data.get('URI')]
        session['added_songs'] = added_songs
    except (ValueError, SyntaxError) as e:
        logger.error(f"Error parsing song data for removal: {e}")

    return redirect(url_for('main_page'))


@app.route('/remove_all')
def remove_all():
    session['added_songs'] = []
    return redirect(url_for('main_page'))


from urllib.parse import urlencode, quote

@app.route('/playlist', methods=['GET', 'POST'])
def admin():
    token = session.get('token_data')
    if not token:
        # Hardcoding the exact URI with port 80 to match Spotify Dashboard
        callback_url = "http://127.0.0.1:80/callback"
        params = {
            'client_id': CLIENT_ID,
            'response_type': 'code',
            'redirect_uri': callback_url,
            'scope': SCOPE
        }
        auth_url = f"https://accounts.spotify.com/authorize?{urlencode(params, quote_via=quote)}"
        logger.info(f"Redirecting to Spotify auth URL: {auth_url}")
        return redirect(auth_url)

    form = PlaylistForm()
    headers = get_auth_header()

    try:
        profile_resp = requests.get('https://api.spotify.com/v1/me', headers=headers, timeout=10)
        profile_resp.raise_for_status()
        my_id = profile_resp.json().get('id')

        if form.validate_on_submit():
            playlist_name = form.playlist.data
            create_resp = requests.post(
                f"https://api.spotify.com/v1/users/{my_id}/playlists",
                headers={**headers, 'Content-Type': 'application/json'},
                json={'name': playlist_name, 'description': 'Spotify wrapped game', 'public': True},
                timeout=10
            )
            create_resp.raise_for_status()
            playlist_id = create_resp.json().get('id')
            return redirect(url_for('set_playlist', playlist_id=playlist_id, nr_tracks=0))

        playlists_resp = requests.get(
            f"https://api.spotify.com/v1/users/{my_id}/playlists",
            headers=headers,
            timeout=10
        )
        playlists_resp.raise_for_status()

        playlists = []
        for item in playlists_resp.json().get("items", []):
            img_url = item["images"][0]["url"] if item.get("images") else None
            playlists.append({
                "name": item.get("name"),
                "id": item.get("id"),
                "img": img_url,
                "nr_tracks": item.get("tracks", {}).get("total", 0)
            })

        return render_template("admin.html", form=form, playlists=playlists)

    except requests.RequestException as e:
        logger.error(f"Error in admin route: {e}")
        if isinstance(e, requests.HTTPError) and e.response.status_code == 401:
            session.pop('token_data', None)
            return redirect(url_for('admin'))

        return redirect(url_for('main_page'))


@app.route('/set_playlist/<playlist_id>/<int:nr_tracks>')
def set_playlist(playlist_id, nr_tracks):
    session['playlist_id'] = playlist_id
    session['index'] = nr_tracks
    session['ready'] = True
    return redirect(url_for('main_page'))


@app.route('/callback')
def callback():
    code = request.args.get('code')
    if not code:
        logger.error("No code provided in callback.")
        return redirect(url_for('main_page'))

    try:
        # Hardcoding the exact URI with port 80 to match Spotify Dashboard
        callback_url = "http://127.0.0.1:80/callback"
        response = requests.post(
            'https://accounts.spotify.com/api/token',
            data={
                "code": code,
                "redirect_uri": callback_url,
                "grant_type": "authorization_code",
                "client_id": CLIENT_ID,
                "client_secret": CLIENT_SECRET,
            },
            timeout=10
        )
        response.raise_for_status()
        session['token_data'] = response.json().get("access_token")
    except requests.RequestException as e:
        logger.error(f"Error fetching token: {e}")

    return redirect(url_for("admin"))


if __name__ == "__main__":
    app.run(debug=False, host='0.0.0.0', port=5000)
