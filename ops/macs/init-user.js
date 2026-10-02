// Same application user and rights as on the old host, new password from the environment.
db = db.getSiblingDB('arenasoldout');
db.createUser({
  user: 'arenasoldout_user',
  pwd: process.env.APP_DB_PASSWORD,
  roles: [{ role: 'readWrite', db: 'arenasoldout' }]
});
print('User arenasoldout_user created');
