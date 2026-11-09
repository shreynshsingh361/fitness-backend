-- People can sign in with Firebase (email and password, or Google). Their
-- Firebase user id ties the Firebase account to the user here.
-- Someone who only ever signed in with Firebase has no password here: their
-- password_hash is '' and bcrypt never matches an empty hash.
ALTER TABLE users ADD COLUMN firebase_uid text UNIQUE;
