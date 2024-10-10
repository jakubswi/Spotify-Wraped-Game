import random, os, requests
from flask import Flask, render_template, redirect, url_for, request
from flask_bootstrap import Bootstrap5
from flask_wtf import FlaskForm, CSRFProtect
from wtforms import StringField, SubmitField
from wtforms.validators import DataRequired


TOKEN_DATA = ''
SCOPE = "playlist-modify-private playlist-modify-public user-read-private user-read-email"

CLIENT_ID=os.environ["CLIENT_ID"]
CLIENT_SECRET=os.environ["CLIENT_SECRET"]
CALLBACK_URL=os.environ["CALLBACK_URL"]


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

index=0


@app.route('/',methods=['GET', 'POST'])
def main_page():
    form = SongForm()
    if form.validate_on_submit():
        song=form.song.data
        response=requests.get('https://api.spotify.com/v1/search',headers={'Authorization': 'Bearer '+TOKEN_DATA},params={'q':song,'type':'track'}).json()
        songs = [{"URI": item["uri"], "name": item["name"], "artists": [artist["name"] for artist in item["artists"]],"img":item["album"]["images"][0]["url"]} for item in response["tracks"]["items"]]
        return render_template("song.html", list_of_songs=songs)
    return render_template("index.html", form=form)

@app.route('/<uri>')
def adding_to_playlist(uri):
    global index
    if index==0:
        response=requests.post(f'https://api.spotify.com/v1/playlists/{playlist_id}/tracks',headers={'Authorization': 'Bearer '+TOKEN_DATA,'Content-Type':'application/json'},json={'uris':[uri],'position':0})
        if response.status_code!=201:
            return response.json()
        else:
            index+=1

    else:
        response=requests.post(f'https://api.spotify.com/v1/playlists/{playlist_id}/tracks',headers={'Authorization': 'Bearer '+TOKEN_DATA,'Content-Type':'application/json'},json={'uris':[uri],'position':random.randint(0,index)})
        if response.status_code != 201:
            return response.json()
        else:
            index += 1

    return redirect(url_for('main_page'))

@app.route('/playlist',methods=['GET', 'POST'])
def admin():
    global playlist_id, index
    if not TOKEN_DATA:
        return redirect(
            f"https://accounts.spotify.com/authorize?client_id={CLIENT_ID}&response_type=code&redirect_uri={CALLBACK_URL}&scope={SCOPE}")
    else:
        index=0
        form=PlaylistForm()

        if form.validate_on_submit():
            playlist_name=form.playlist.data
            my_id=requests.get('https://api.spotify.com/v1/me',headers={'Authorization': 'Bearer '+TOKEN_DATA}).json()['id']
            playlist_id=requests.post(f"https://api.spotify.com/v1/users/{my_id}/playlists",headers={'Authorization': 'Bearer '+TOKEN_DATA,'Content-Type':'application/json'},json={'name':playlist_name,'description':'Spotify wrapped game','public':True}).json()['id']


            return redirect(url_for('main_page'))
        return render_template("admin.html", form=form)

@app.route('/callback/')
def callback():
    global TOKEN_DATA
    CODE=request.args['code']
    TOKEN_DATA= requests.post('https://accounts.spotify.com/api/token',
                             data={"code": CODE,
                                   "redirect_uri": CALLBACK_URL,
                                   "grant_type": "authorization_code",
                                   "client_id": CLIENT_ID,
                                   "client_secret": CLIENT_SECRET,
                                   }).json()["access_token"]
    return redirect(url_for("admin"))




if __name__ == "__main__":
    app.run(debug=False)