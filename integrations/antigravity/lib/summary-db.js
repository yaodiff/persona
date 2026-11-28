import{statSync}from"node:fs";import{DatabaseSync}from"node:sqlite";
// Reusable read-only gate for agy's conversation_summaries.db. The connection is
// reused until the file is replaced (dev/ino change) or a statement fails, and
// the parsed full snapshot is reused while the db and its -wal stat signature
// stay unchanged, so watch polls stop reopening and rescanning the database.
const ALL_SQL=`SELECT conversation_id, status, not_fully_idle, killed, step_count,
               title, workspace_uris, last_modified_time
        FROM conversation_summaries`,ONE_SQL=`SELECT conversation_id, status, not_fully_idle, killed, step_count,
               title, workspace_uris, last_modified_time
        FROM conversation_summaries WHERE conversation_id = ?`;
export function summaryFileSignature(target){try{const s=statSync(target,{bigint:!0});return`${s.dev}:${s.ino}:${s.size}:${s.mtimeNs}`}catch{return"-"}}
export function summaryStamp(dbPath){return`${summaryFileSignature(dbPath)}|wal:${summaryFileSignature(dbPath+"-wal")}`}
function shapedRow(row){let workspaceUris=[];try{const parsed=JSON.parse(row.workspace_uris);Array.isArray(parsed)&&(workspaceUris=parsed.filter(uri=>typeof uri=="string"&&uri))}catch{}return{...row,WorkspaceURIs:workspaceUris}}
export function createSummaryStateStore(dbPath){
  const stats={opens:0,closes:0,fullScans:0,pointLookups:0,cacheHits:0,reopens:0,probes:0};
  let db,identity,allStmt,oneStmt,probeStmt,snapshot,snapshotStamp;
  function fileIdentity(){try{const s=statSync(dbPath,{bigint:!0});return{id:`${s.dev}:${s.ino}`,exists:!0}}catch{return{id:"",exists:!1}}}
  function closeConnection(){if(db){try{db.close()}catch{}stats.closes++}db=void 0,allStmt=void 0,oneStmt=void 0,probeStmt=void 0,identity=void 0}
  function connection(){
    const{id,exists}=fileIdentity();
    if(!exists){closeConnection();return}
    if(db&&identity===id)return db;
    if(db){closeConnection();stats.reopens++}
    const opened=new DatabaseSync(dbPath,{readOnly:!0});
    try{
      opened.exec("PRAGMA query_only = 1; PRAGMA busy_timeout = 50");
      const nextAll=opened.prepare(ALL_SQL),nextOne=opened.prepare(ONE_SQL),nextProbe=opened.prepare("SELECT 1 FROM conversation_summaries LIMIT 1");
      db=opened,identity=id,allStmt=nextAll,oneStmt=nextOne,probeStmt=nextProbe,stats.opens++;
      return db;
    }catch(error){try{opened.close()}catch{}throw error}
  }
  // Returns the cached Map when the db/-wal signature still matches, otherwise
  // scans every row. A missing database resolves to an empty cached Map.
  function states(){
    const stamp=summaryStamp(dbPath);
    if(snapshot&&stamp===snapshotStamp){stats.cacheHits++;return snapshot}
    try{
      const conn=connection();
      if(!conn){snapshot=new Map,snapshotStamp=stamp;return snapshot}
      const map=new Map(allStmt.all().map(row=>[row.conversation_id,shapedRow(row)]));
      stats.fullScans++,snapshot=map,snapshotStamp=stamp;
      return map;
    }catch(error){snapshot=void 0,snapshotStamp=void 0,closeConnection();throw error}
  }
  function cachedStates(){if(snapshot&&summaryStamp(dbPath)===snapshotStamp)return snapshot}
  // A cache hit performs no SQL. Inventory reads that must preserve agy's own
  // lock/IO failure contract call checkedStates(): the constant-cost probe still
  // throws SQLITE_BUSY while another connection holds an exclusive lock.
  function probe(){try{const conn=connection();if(conn){probeStmt.get();stats.probes++}}catch(error){closeConnection();throw error}}
  function checkedStates(){const map=states();probe();return map}
  // Fresh snapshots serve the Map; a cold/stale cache costs one primary-key read.
  function get(rawId){const fresh=cachedStates();if(fresh)return fresh.get(rawId);return point(rawId)}
  function point(rawId){try{
    const conn=connection();
    if(!conn)return;
    const row=oneStmt.get(rawId);
    stats.pointLookups++;
    return row?shapedRow(row):void 0;
  }catch(error){closeConnection();throw error}}
  function close(){closeConnection(),snapshot=void 0,snapshotStamp=void 0}
  return{states,checkedStates,cachedStates,get,point,probe,close,stamp:()=>summaryStamp(dbPath),stats,get connected(){return db!==void 0}};
}
