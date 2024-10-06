import random, os
from flask import Flask, render_template, redirect, url_for, session
import spotipy
from spotipy import FlaskSessionCacheHandler
from spotipy.oauth2 import SpotifyOAuth
from flask_bootstrap import Bootstrap5
from flask_wtf import FlaskForm, CSRFProtect
from wtforms import StringField, SubmitField
from wtforms.validators import DataRequired


scope = ["playlist-modify-private", "playlist-modify-public"]
CLIENT_ID=os.environ["CLIENT_ID"]
CLIENT_SECRET=os.environ["CLIENT_SECRET"]
redirect_uri=os.environ["REDIRECT_URL"]


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



@app.route('/',methods=['GET', 'POST'])
def main_page():
    form = SongForm()
    if form.validate_on_submit():
        song=form.song.data
        response=sp.search(song)
        songs = [{"URI": item["uri"], "name": item["name"], "artists": [artist["name"] for artist in item["artists"]],"img":item["album"]["images"][0]["url"]} for item in response["tracks"]["items"]]

        return render_template("song.html", list_of_songs=songs)
    return render_template("index.html", form=form)

@app.route('/<URI>')
def adding_to_playlist(URI):
    if session['index']==0:
        sp.playlist_add_items(session['PLAYLIST_ID'],[URI])
        session['index']+=1
    else:
        sp.playlist_add_items(session['PLAYLIST_ID'], [URI], random.randint(0, session.get('index')))
        session['index']+=1

    return redirect(url_for("main_page"))

@app.route('/jakub',methods=['GET', 'POST'])
def admin():
    global sp
    session['index']=0
    form=PlaylistForm()
    sp = spotipy.Spotify(
        auth_manager=SpotifyOAuth(client_secret=CLIENT_SECRET, redirect_uri=redirect_uri, client_id=CLIENT_ID,
                                  scope=scope,cache_handler=FlaskSessionCacheHandler(session)))

    if form.validate_on_submit():
        playlist_name=form.playlist.data
        session['my_id']=sp.me()['id']
        session['PLAYLIST_ID']=sp.user_playlist_create(session.get('my_id'),playlist_name,public=True, collaborative=False, description='Spotify Wrpped Game')['id']
        return redirect(url_for('main_page'))


    return render_template("admin.html", form=form)



if __name__ == "__main__":
    app.run(debug=False)