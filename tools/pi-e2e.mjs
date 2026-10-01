// 本机专用：真实 Control、Worker、SSH、官方 Pi SDK，模型仅访问临时回环 mock。
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { ControlHarness } from './mobile-e2e/lib/control.mjs'
import { freePort, output, run, startProcess } from './mobile-e2e/lib/process.mjs'
import { SSHProtocolClient } from './mobile-e2e/lib/ssh-protocol.mjs'

const repo=resolve(fileURLToPath(new URL('..',import.meta.url)))
const adapter=resolve(repo,'../claude-codex')
const root=await mkdtemp('/tmp/tyrs-pi-e2e-')
const control=new ControlHarness({repoRoot:repo,runDir:resolve(root,'control'),label:'pi'})
const clients=[]
let worker
let requestCount=0
const mock=createServer(async(req,res)=>{
  const chunks=[];for await(const chunk of req)chunks.push(chunk)
  const body=JSON.parse(Buffer.concat(chunks).toString())
  requestCount++
  const text=body.messages?.findLast(m=>m.role==='user')?.content??''
  res.writeHead(200,{'content-type':'text/event-stream'})
  const chunk={id:`pi-test-${requestCount}`,object:'chat.completion.chunk',created:1,model:body.model}
  res.write(`data: ${JSON.stringify({...chunk,choices:[{index:0,delta:{role:'assistant',content:`Pi 原生引擎已收到：${typeof text==='string'?text:JSON.stringify(text)}`},finish_reason:null}]})}\n\n`)
  res.write(`data: ${JSON.stringify({...chunk,choices:[{index:0,delta:{},finish_reason:'stop'}],usage:{prompt_tokens:12,completion_tokens:8,total_tokens:20}})}\n\n`)
  res.end('data: [DONE]\n\n')
})
const quote=value=>"'"+value.replaceAll("'","'\\''")+"'"
try {
  await new Promise(resolve=>mock.listen(0,'127.0.0.1',resolve))
  await control.start()
  const registration=await control.admin.createWorker('pi-local-acceptance')
  const home=resolve(root,'home'),workspace=resolve(root,'project'),state=resolve(root,'state')
  const agent=resolve(home,'.pi/agent'),codexHome=resolve(home,'.codex')
  for(const path of [agent,codexHome,workspace,resolve(root,'tmp')])await mkdir(path,{recursive:true})
  await writeFile(resolve(workspace,'README.md'),'Pi 本地验收项目\n')
  run('git',['init','--quiet',workspace])
  const modelURL=`http://127.0.0.1:${mock.address().port}/v1`
  await writeFile(resolve(agent,'models.json'),JSON.stringify({providers:{gate:{baseUrl:modelURL,api:'openai-completions',apiKey:'test-only',models:[{
    id:'gate',name:'Pi 本地验收',reasoning:false,input:['text'],contextWindow:32000,maxTokens:1024,cost:{input:0,output:0,cacheRead:0,cacheWrite:0},
  }]}}}))
  await writeFile(resolve(agent,'settings.json'),JSON.stringify({defaultProvider:'gate',defaultModel:'gate',retry:{enabled:false}}))
  await writeFile(resolve(codexHome,'config.toml'),`model="mock-model"\nmodel_provider="mock"\napproval_policy="never"\n[model_providers.mock]\nname="Mock"\nbase_url=${JSON.stringify(modelURL)}\nwire_api="responses"\nsupports_websockets=false\n`)
  const key=resolve(root,'client-key'),known=resolve(root,'known_hosts')
  run('ssh-keygen',['-q','-t','ed25519','-N','','-f',key])
  await writeFile(resolve(root,'authorized_keys'),await readFile(key+'.pub'),{mode:0o600})
  const bin=resolve(root,'worker'),pi=resolve(root,'pi-codex'),claude=resolve(root,'claude-codex')
  run('go',['build','-o',bin,'./cmd/tyrs-hand-worker'],{cwd:repo})
  run('npm',['--prefix','packages/pi','run','build'],{cwd:adapter})
  run('npm',['run','build'],{cwd:adapter})
  await writeFile(pi,`#!/bin/sh\nexec ${quote(process.execPath)} ${quote(resolve(adapter,'packages/pi/dist/pi/src/adapter.mjs'))} "$@"\n`,{mode:0o700})
  await writeFile(claude,`#!/bin/sh\nexec ${quote(process.execPath)} ${quote(resolve(adapter,'dist/src/adapter.mjs'))} "$@"\n`,{mode:0o700})
  const codexPort=await freePort(),claudePort=await freePort(),piPort=Number(process.env.TYRS_PI_TEST_PORT??3334)
  const env={PATH:process.env.PATH,HOME:home,TMPDIR:resolve(root,'tmp'),LANG:'en_US.UTF-8',
    PI_CODING_AGENT_DIR:agent,PI_CLI:resolve(adapter,'packages/pi/node_modules/.bin/pi'),PI_ADAPTER_DEBUG:'1',
    TYRS_HAND_WORKER_ID:registration.worker.id,TYRS_HAND_WORKER_ROLE:'discord',TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS:'2',
    TYRS_HAND_WORKER_HOME:home,TYRS_HAND_WORKER_CODEX_HOME:codexHome,TYRS_HAND_WORKER_DATA_ROOT:state,
    TYRS_HAND_WORKER_WORKSPACE_ROOT:workspace,TYRS_HAND_WORKER_SHELL:'/bin/sh',
    TYRS_HAND_WORKER_CREDENTIAL_FILE:resolve(root,'credential'),TYRS_HAND_WORKER_ENROLLMENT_TOKEN:registration.enrollmentToken,
    TYRS_HAND_WORKER_AUTHORIZED_KEYS_FILE:resolve(root,'authorized_keys'),
    TYRS_HAND_WORKER_SSH_HOST_KEY_FILE:resolve(state,'ssh/host_key'),TYRS_HAND_WORKER_SSH_LISTEN_ADDR:`127.0.0.1:${codexPort}`,
    TYRS_HAND_WORKER_PI_ENABLED:'true',TYRS_HAND_WORKER_PI_BIN:pi,TYRS_HAND_WORKER_PI_SSH_LISTEN_ADDR:`127.0.0.1:${piPort}`,
    TYRS_HAND_WORKER_CLAUDE_ENABLED:'true',TYRS_HAND_WORKER_CLAUDE_BIN:claude,TYRS_HAND_WORKER_CLAUDE_SSH_LISTEN_ADDR:`127.0.0.1:${claudePort}`,
    TYRS_HAND_WORKER_CLAUDE_CLI:process.env.TYRS_HAND_TEST_CLAUDE_CLI??'claude',
    TYRS_HAND_CODEX_BIN:output('which',['codex']),TYRS_HAND_WORKER_CONTROL_URL:control.baseURL,
    TYRS_HAND_WORKER_GLOBAL_ENV_FILE:resolve(root,'codex.env'),TYRS_HAND_WORKER_ENV_FILE:resolve(root,'worker.env'),
    TYRS_HAND_SSH_AGENT_DIR:resolve(root,'ssh-agent'),TYRS_HAND_HEARTBEAT_INTERVAL:'1s',TYRS_HAND_NODE_HEARTBEAT_INTERVAL:'1s',
    TYRS_HAND_WORKER_SYNC_FALLBACK_INTERVAL:'1s',TYRS_HAND_CONTROL_TIMEOUT:'5s',
  }
  worker=await startProcess('pi-worker',bin,[],{cwd:workspace,env,inheritEnv:false,logDir:resolve(root,'logs')})
  try {await control.admin.waitForRuntime(registration.worker.id,'pi')}
  catch(error){await worker.stop();console.error(await readFile(resolve(root,'logs/pi-worker.log'),'utf8'));throw error}
  for(const engine of ['codex','claude-code'])await control.admin.waitForRuntime(registration.worker.id,engine)
  const publicKey=[piPort,codexPort,claudePort].map(port=>output('ssh-keyscan',['-p',String(port),'127.0.0.1'])).join('\n')
  await writeFile(known,publicKey+'\n')
  const harness={adapter,sshArguments(engine,command){return ['-F','/dev/null','-o','BatchMode=yes','-o','IdentitiesOnly=yes',
    '-o','StrictHostKeyChecking=yes','-o',`UserKnownHostsFile=${known}`,'-i',key,'-p',String({pi:piPort,codex:codexPort,'claude-code':claudePort}[engine]),'developer@127.0.0.1',command]}}
  const desktop=await new SSHProtocolClient(harness,'pi').open();clients.push(desktop)
  const mobile=await new SSHProtocolClient(harness,'pi').open();clients.push(mobile)
  const entryVersion=output('ssh',harness.sshArguments('pi','codex --version')).match(/^codex-cli (\S+)/)?.[1]
  const initialization=desktop.trace.find(item=>item.direction==='server'&&item.result?.userAgent)?.result
  assert.equal(entryVersion,'0.157.1')
  assert.equal(initialization?.userAgent.match(/^[^/]+\/([^ ]+)/)?.[1],entryVersion,
    'Desktop initialize 版本必须与真实 SSH 入口 codex --version 一致')
  const identity=await desktop.request('runtime/info')
  assert.equal(identity.engine,'pi')
  const {thread}=await desktop.request('thread/start',{cwd:workspace,model:'gate/gate'})
  await mobile.request('thread/resume',{threadId:thread.id})
  for(const [sender,marker] of [[desktop,'PI_DESKTOP_SSH'],[mobile,'PI_MOBILE_SSH']]) {
    const {turn}=await sender.request('turn/start',{threadId:thread.id,input:[{type:'text',text:marker}],clientUserMessageId:marker})
    for(const client of clients){const result=(await client.waitFor('turn/completed',p=>p.turn.id===turn.id)).params.turn;
      if(result.status!=='completed'){await worker.stop();console.error(await readFile(resolve(root,'logs/pi-worker.log'),'utf8'))}
      assert.equal(result.status,'completed',JSON.stringify(result.error))}
  }
  assert.equal(requestCount,2,'两端各一次，标题不能额外调用模型')
  const history=(await mobile.request('thread/read',{threadId:thread.id,includeTurns:true})).thread
  assert.equal(history.turns.length,2)
  const otherClients=[]
  for(const engine of ['codex','claude-code']) {
    const client=await new SSHProtocolClient(harness,engine).open()
    clients.push(client);otherClients.push([engine,client])
    assert.equal((await client.request('runtime/info')).engine,engine)
    assert.ok(!(await client.request('thread/list',{})).data.some(t=>t.id===thread.id),'Pi 会话不能串到其他引擎')
  }
  await control.admin.request(`/workers/${registration.worker.id}/runtimes/pi/restart`,{method:'POST',csrf:true})
  await control.admin.waitForRuntime(registration.worker.id,'pi')
  const resumed=await new SSHProtocolClient(harness,'pi').open();clients.push(resumed)
  await resumed.request('thread/resume',{threadId:thread.id})
  const recovered=(await resumed.request('thread/read',{threadId:thread.id,includeTurns:true})).thread
  assert.deepEqual(recovered.turns.map(t=>({id:t.id,items:t.items.map(i=>i.id)})),
    history.turns.map(t=>({id:t.id,items:t.items.map(i=>i.id)})),'重启后回合与条目 ID 保持一致')
  for(const [engine,client] of otherClients) {
    assert.equal((await client.request('runtime/info')).engine,engine,'Pi 重启不能中断其他引擎的现有连接')
  }
  assert.equal(requestCount,2)
  // 直接启动官方 SDK 适配器，不经过 wire 录制器；只杀本轮独立 socket 对应的进程。
  const recoveryDurations=[]
  for(const connected of [true,false]) {
    if(!connected)for(const client of clients.filter(item=>item.engine==='pi'))await client.close()
    const socket=resolve(state,'pi/app-server.sock')
    const processes=output('ps',['-axo','pid=,command=']).split('\n').filter(line=>
      line.includes(resolve(adapter,'packages/pi/dist/pi/src/adapter.mjs'))&&line.includes(`--listen unix://${socket}`))
    assert.equal(processes.length,1,'只能向本轮唯一 Pi 适配器发送 SIGKILL')
    const killed=Number(processes[0].trim().split(/\s+/)[0]),started=Date.now()
    process.kill(killed,'SIGKILL')
    let healthy,lastError
    while(Date.now()-started<20000) {
      const candidate=new SSHProtocolClient(harness,'pi')
      try {
        await candidate.open()
        assert.equal((await candidate.request('runtime/info')).engine,'pi')
        healthy=candidate;clients.push(candidate);break
      } catch(error) {lastError=error;await candidate.close()}
      await new Promise(resolve=>setTimeout(resolve,200))
    }
    if(!healthy){await worker.stop();console.error(await readFile(resolve(root,'logs/pi-worker.log'),'utf8'));throw lastError??new Error('Pi SIGKILL 恢复超时')}
    await healthy.request('thread/resume',{threadId:thread.id})
    const replay=(await healthy.request('thread/read',{threadId:thread.id,includeTurns:true})).thread
    assert.deepEqual(replay.turns.map(t=>({id:t.id,items:t.items.map(i=>i.id)})),
      history.turns.map(t=>({id:t.id,items:t.items.map(i=>i.id)})))
    const {turn}=await healthy.request('turn/start',{threadId:thread.id,input:[{type:'text',text:`PI_AFTER_SIGKILL_${connected}`}],clientUserMessageId:`sigkill:${connected}`})
    assert.equal((await healthy.waitFor('turn/completed',p=>p.turn.id===turn.id)).params.turn.status,'completed')
    history.turns=(await healthy.request('thread/read',{threadId:thread.id,includeTurns:true})).thread.turns
    for(const [engine,client]of otherClients)assert.equal((await client.request('runtime/info')).engine,engine)
    recoveryDurations.push({connected,milliseconds:Date.now()-started})
  }
  assert.equal(requestCount,4,'两次 SIGKILL 后各执行一次，无额外模型调用')
  console.log(JSON.stringify({result:'SSH 双端、版本握手与两次 Pi SIGKILL 恢复通过',recoveryDurations,root,control:control.baseURL,port:piPort,threadId:thread.id,workspace}))
  if(process.argv.includes('--serve')) {
    await writeFile(resolve(root,'ssh-config'),`Host tyrs-pi-local\n  HostName 127.0.0.1\n  Port ${piPort}\n  User developer\n  IdentityFile ${key}\n  UserKnownHostsFile ${known}\n  IdentitiesOnly yes\n  StrictHostKeyChecking yes\n`)
    console.log(`GUI 验收 SSH 配置：${root}/ssh-config`)
    await new Promise(resolve=>{process.once('SIGTERM',resolve);process.once('SIGINT',resolve)})
  }
} finally {
  for(const client of clients)await client.close()
  // 每个引擎最多用 5 秒关闭；给三个独立进程留足清理时间，避免测试器先杀 Worker。
  await worker?.stop({timeoutMs:20000})
  await control.stop()
  mock.closeAllConnections();await new Promise(resolve=>mock.close(resolve))
  await rm(root,{recursive:true,force:true})
}
