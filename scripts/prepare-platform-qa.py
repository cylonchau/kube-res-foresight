from pathlib import Path
import sqlite3, json, shutil
root=Path(__file__).resolve().parents[1]
dest=root/'target/platform-qa'
dest.mkdir(parents=True,exist_ok=True)
db=dest/'schema-upgrade.db'
if not db.exists() and (root/'kubeforecast.db').exists():
 source=sqlite3.connect((root/'kubeforecast.db').as_uri()+'?mode=ro',uri=True)
 schemas=source.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL").fetchall()
 target=sqlite3.connect(db)
 for (sql,) in schemas:target.execute(sql)
 target.commit();target.close();source.close()
 print('Scratch database initialized with existing table schemas:',len(schemas))
(dest/'config.yaml').write_text('''app_name: kubeforecast-platform-qa
database_driver: sqlite
sqlite:
  file: target/platform-qa/schema-upgrade.db
  max_open_connection: "1"
  max_idle_connection: "1"
server:
  port: 20111
  dev_mode: true
redis:
  addr: ""
telegram:
  enabled: false
''',encoding='utf-8')
embed=(root/'pkg/app/router/dist').resolve()
assert embed.is_relative_to(root) and embed.name=='dist'
if embed.exists():shutil.rmtree(embed)
shutil.copytree(root/'web/dist',embed)
(embed/'.gitkeep').write_text('',encoding='utf-8')
print('Packaged current admin for backend verification')
