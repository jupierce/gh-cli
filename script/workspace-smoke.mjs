import assert from 'node:assert/strict';
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';

// Linux-only black-box qualification. All API responses and Git repositories are
// local fixtures; Git network commands stop at the wrapper before connecting.
const gh = path.resolve(process.argv[2] || 'bin/gh');
assert.ok(fs.existsSync(gh), 'build bin/gh before running this fixture');
const root = fs.mkdtempSync('/tmp/gh-ws-smoke-');
for (const dir of ['home','config','repo','shim']) fs.mkdirSync(path.join(root,dir));
const env = {PATH:`${root}/shim:${process.env.PATH}`, HOME:`${root}/home`, GH_CONFIG_DIR:`${root}/config`,
  GH_WORKSPACE_CONFIG:`${root}/workspace.json`, GH_HOST:'github.com', GH_PROMPT_DISABLED:'1',
  GH_NO_UPDATE_NOTIFIER:'1', GH_NO_EXTENSION_UPDATE_NOTIFIER:'1', GH_TELEMETRY:'false',
  GIT_CONFIG_NOSYSTEM:'1', GIT_CONFIG_GLOBAL:`${root}/gitconfig`, NO_COLOR:'1', GH_PAGER:'cat',
  DBUS_SESSION_BUS_ADDRESS:`unix:path=${root}/no-keyring`, HTTPS_PROXY:'http://127.0.0.1:1'};
const operations=['pull_request.create','pull_request.edit','issue.create','issue.edit','comment.create','pull_request.review','repository.fork'];
fs.writeFileSync(env.GH_WORKSPACE_CONFIG, JSON.stringify({protocol:'chai.github-proxy/v1',host:'github.com',socket:`${root}/api.sock`,git_proxy_prefixes:['http://localhost:8080/github.com/']}));
fs.writeFileSync(`${root}/shim/git`, `#!/bin/sh
for arg do
  case "$arg" in fetch|push|clone)
    printf '%s\\n' "$*" >> '${root}/git-network.txt'
    echo FIXTURE_STOP_BEFORE_GIT_NETWORK >&2
    exit 71;;
  esac
done
exec /usr/bin/git "$@"
`, {mode:0o755});
const requests=[], results=[];
let pending=false;
const server=http.createServer(async(req,res)=>{
  let raw=''; for await(const chunk of req) raw+=chunk;
  assert.equal(req.headers.authorization,undefined);
  assert.equal(req.headers.cookie,undefined);
  assert.equal(req.headers.host,'api.github.com');
  const body=raw?JSON.parse(raw):null;
  requests.push({method:req.method,path:req.url,body});
  res.setHeader('Content-Type','application/json');
  if(req.url==='/_chai/capabilities') {res.end(JSON.stringify({protocol:'chai.github-proxy/v1',ready:true,host:'github.com',generation:'g1',login:'chai-bot',operations}));return;}
  assert.equal(req.headers['x-chai-workspace-generation'],'g1');
  if(req.url.startsWith('/_chai/operations/')) {
    const operation=req.url.split('/').at(-1);
    let result={id:'PR_42',url:'https://github.com/acme/project/pull/42'};
    if(operation.startsWith('issue.')) result={id:'I_42',url:'https://github.com/acme/project/issues/42'};
    if(operation==='repository.fork') result={id:'R_fork',name:'acme_project',owner:{login:'chai-bot'},createdAt:new Date().toISOString()};
    res.end(JSON.stringify({status:pending?'pending':'completed',result}));return;
  }
  if(req.url==='/graphql') {
    assert.ok(!/^\s*mutation\b/.test(body.query),'native mutation must not be used');
    if(body.query.includes('ProjectItems')) {
      const key=body.query.includes('PullRequestProjectItems')?'pullRequest':'issue';
      res.end(JSON.stringify({data:{repository:{[key]:{projectItems:{totalCount:0,nodes:[],pageInfo:{hasNextPage:false,endCursor:''}}}}}}));return;
    }
    const pr={id:'PR_42',number:42,title:'Fixture PR',body:'Fixture body',state:'OPEN',url:'https://github.com/acme/project/pull/42',
      headRefName:'topic',baseRefName:'main',isCrossRepository:false,headRepositoryOwner:{login:'acme'},
      headRepository:{id:'R_1',name:'project',nameWithOwner:'acme/project',owner:{login:'acme'}},
      baseRepository:{id:'R_1',name:'project',nameWithOwner:'acme/project',owner:{login:'acme'}},comments:{nodes:[],totalCount:0}};
    const repo={id:'R_1',name:'project',nameWithOwner:'acme/project',owner:{login:'acme'},defaultBranchRef:{name:'main'},viewerPermission:'WRITE',hasIssuesEnabled:true,
      pullRequest:pr,pullRequests:{nodes:[],totalCount:0},issue:{...pr,id:'I_42',url:'https://github.com/acme/project/issues/42'}};
    const data={viewer:{login:'chai-bot'},repository:repo,__type:{fields:[]}};
    for(const m of body.query.matchAll(/(repo_\d+):/g)) data[m[1]]=repo;
    res.end(JSON.stringify({data}));return;
  }
  if(req.url.startsWith('/items')) {
    if(!req.url.includes('page=2')) res.setHeader('Link','<https://api.github.com/items?page=2>; rel="next"');
    res.end(JSON.stringify([{page:req.url.includes('page=2')?2:1}]));return;
  }
  res.end(JSON.stringify({login:'chai-bot',id:1}));
});
await new Promise(r=>server.listen(`${root}/api.sock`,r));
async function run(label,bin,args,cwd=`${root}/repo`) {
  const start=requests.length;
  const result=await new Promise((resolve,reject)=>{
    const p=spawn(bin,args,{cwd,env});let stdout='',stderr='';
    p.stdout.on('data',s=>stdout+=s);p.stderr.on('data',s=>stderr+=s);p.on('error',reject);
    const timer=setTimeout(()=>p.kill('SIGTERM'),15000);
    p.on('close',code=>{clearTimeout(timer);resolve({label,code,stdout:stdout.trim(),stderr:stderr.trim(),requests:requests.slice(start)});});
  });
  results.push(result);return result;
}
let succeeded=false;
try {
  for(const args of [['init','-b','main'],['config','user.name','Fixture'],['config','user.email','fixture@example.invalid'],['-c','commit.gpgsign=false','commit','--allow-empty','-m','Fixture'],['remote','add','origin','https://github.com/acme/project.git'],['config','--global','url.http://localhost:8080/github.com/.insteadOf','https://github.com/']]) {
    assert.equal((await run('setup','/usr/bin/git',args)).code,0);
  }
  const commands=[
    ['version',['--version']],
    ['token-free REST',['api','user','--jq','.login']],
    ['pagination',['api','items','--paginate','--jq','.[].page']],
    ['implicit repo',['pr','view','42','--json','number,title']],
    ['auth status',['auth','status','--json','hosts']],
    ['PR create',['pr','create','--head','acme:topic','--base','main','--title','Test','--body','Body']],
    ['issue create',['issue','create','--title','Test','--body','Body']],
    ['comment',['pr','comment','42','--body','Comment']],
    ['review',['pr','review','42','--comment','--body','Comment']],
    ['PR edit',['pr','edit','42','--body','Updated']],
    ['issue edit',['issue','edit','42','--body','Updated']],
    ['fork',['repo','fork','acme/project','--clone=false']],
  ];
  for(const [label,args] of commands) {
    const r=await run(label,gh,args);
    assert.equal(r.code,0,`${label}: ${r.stderr}`);
  }
  const token=await run('auth token refused',gh,['auth','token']);
  assert.notEqual(token.code,0);assert.equal(token.stdout,'');
  const checkout=await run('checkout uses Git wrapper',gh,['pr','checkout','42']);
  assert.notEqual(checkout.code,0);assert.match(checkout.stderr,/FIXTURE_STOP_BEFORE_GIT_NETWORK/);
  assert.match(fs.readFileSync(`${root}/git-network.txt`,'utf8'),/fetch.*origin/);
  pending=true;
  const pendingWrite=await run('pending write rejected',gh,['pr','comment','42','--body','Comment']);
  assert.notEqual(pendingWrite.code,0);assert.equal(pendingWrite.stdout,'');
  assert.equal(pendingWrite.requests.filter(r=>r.path.startsWith('/_chai/operations/')).length,1);
  console.log(JSON.stringify({root,passed:results.filter(r=>r.label!=='setup').map(({requests,...r})=>({...r,requestPaths:requests.map(x=>x.path)}))},null,2));
  succeeded=true;
} finally {
  await new Promise(resolve=>server.close(resolve));
  if(succeeded) fs.rmSync(root,{recursive:true,force:true});
  else {
    fs.writeFileSync(`${root}/results.json`,JSON.stringify(results,null,2));
    console.error(`Fixture evidence retained at ${root}/results.json`);
  }
}
