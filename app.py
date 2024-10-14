import ast
import os
import random
import requests

from flask import Flask, render_template, redirect, url_for, request, session
from flask_bootstrap import Bootstrap5
from flask_wtf import FlaskForm, CSRFProtect
from wtforms import StringField, SubmitField
from wtforms.validators import DataRequired

TOKEN_DATA = ''
SCOPE = "playlist-modify-private playlist-modify-public user-read-private user-read-email"
CLIENT_ID = os.environ["CLIENT_ID"]
CLIENT_SECRET = os.environ["CLIENT_SECRET"]
CALLBACK_URL = os.environ["CALLBACK_URL"]


class SongForm(FlaskForm):
    song = StringField('Song', validators=[DataRequired()])
    submit = SubmitField('Serach')


class PlaylistForm(FlaskForm):
    playlist = StringField('Playlist', validators=[DataRequired()])
    submit = SubmitField('Done')


app = Flask(__name__)
Bootstrap5(app)
csrf = CSRFProtect(app)
app.secret_key = os.environ["SECRET_KEY"]
index = 0


@app.route('/', methods=['GET', 'POST'])
def main_page():
    form = SongForm()
    if form.validate_on_submit():
        song = form.song.data
        response = requests.get('https://api.spotify.com/v1/search', headers={'Authorization': 'Bearer ' + TOKEN_DATA},
                                params={'q': song, 'type': 'track'}).json()
        songs = [{"URI": item["uri"], "name": item["name"], "artists": [artist["name"] for artist in item["artists"]],
                  "img": item["album"]["images"][0]["url"]} for item in response["tracks"]["items"]]
        return render_template("song.html", list_of_songs=songs)
    return render_template("index.html", form=form, added_songs=session.get('added_songs'))


@app.route('/adding_to_playlist')
def adding_to_playlist():
    global index
    list_of_songs_to_add = session.get('added_songs')
    if index == 0:
        requests.post(f'https://api.spotify.com/v1/playlists/{playlist_id}/tracks',
                      headers={'Authorization': 'Bearer ' + TOKEN_DATA, 'Content-Type': 'application/json'},
                      json={'uris': [list_of_songs_to_add[0]['URI']], 'position': 0})
        list_of_songs_to_add.pop(0)
        index += 1
        for song in list_of_songs_to_add:
            requests.post(f'https://api.spotify.com/v1/playlists/{playlist_id}/tracks',
                          headers={'Authorization': 'Bearer ' + TOKEN_DATA, 'Content-Type': 'application/json'},
                          json={'uris': [song['URI']], 'position': random.randint(0, index)})
            index += 1
    else:
        for song in list_of_songs_to_add:
            requests.post(f'https://api.spotify.com/v1/playlists/{playlist_id}/tracks',
                          headers={'Authorization': 'Bearer ' + TOKEN_DATA, 'Content-Type': 'application/json'},
                          json={'uris': [song['URI']], 'position': random.randint(0, index)})
            index += 1
    session['added_songs'] = []

    return redirect(url_for('main_page'))


@app.route('/adding_to_list/')
def adding_to_list():
    if session.get('added_songs') is not None:
        lista = session.get('added_songs')
        data = ast.literal_eval(request.args["song"])
        lista.append(data)
        session['added_songs'] = lista
        return redirect(url_for('main_page'))
    else:
        session['added_songs'] = [ast.literal_eval(request.args['song'])]
        return redirect(url_for('main_page'))


@app.route('/removing_from_list')
def removing_from_list():
    session['added_songs'] = session.get('added_songs').remove(ast.literal_eval(request.args['song']))
    return redirect(url_for('main_page'))


@app.route('/playlist', methods=['GET', 'POST'])
def admin():
    global playlist_id, index
    session['added_songs'] = []
    if not TOKEN_DATA:
        return redirect(
            f"https://accounts.spotify.com/authorize?client_id={CLIENT_ID}&response_type=code&redirect_uri={CALLBACK_URL}&scope={SCOPE}")
    else:
        index = 0
        form = PlaylistForm()
        if form.validate_on_submit():
            playlist_name = form.playlist.data
            my_id = \
            requests.get('https://api.spotify.com/v1/me', headers={'Authorization': 'Bearer ' + TOKEN_DATA}).json()[
                'id']
            playlist_id = requests.post(f"https://api.spotify.com/v1/users/{my_id}/playlists",
                                        headers={'Authorization': 'Bearer ' + TOKEN_DATA,
                                                 'Content-Type': 'application/json'},
                                        json={'name': playlist_name, 'description': 'Spotify wrapped game',
                                              'public': True}).json()['id']
            return redirect(url_for('main_page'))
        return render_template("admin.html", form=form)


@app.route('/callback/')
def callback():
    global TOKEN_DATA
    TOKEN_DATA = requests.post('https://accounts.spotify.com/api/token',
                               data={"code": request.args['code'],
                                     "redirect_uri": CALLBACK_URL,
                                     "grant_type": "authorization_code",
                                     "client_id": CLIENT_ID,
                                     "client_secret": CLIENT_SECRET,
                                     }).json()["access_token"]
    return redirect(url_for("admin"))


if __name__ == "__main__":
    app.run(debug=False)
