## Go Ccmchain
### 部署
1. 安装go语言环境

2. 编译客户端<br/>
`cd go-ccmchain && make gccm`

3. 初始化数据<br/> 
`./build/bin/gccm init --datadir /data/datadir genesis.json`

4. 后台启动客户端<br/>`nohup ./build/bin/gccm --gcmode archive --datadir /data/datadir --keystore /data/keystore --rpc --rpcapi db,ccm,net,web3,personal,admin,miner --txpool.lifetime 10m0s --rpcport 7575 --rpcaddr 0.0.0.0 --ipcpath /data/datadir/gccm.ipc --rpccorsdomain * &`

5. 连接至javascript控制台<br/>`./build/bin/gccm --datadir /data/datadir --keystore /data/keystore --rpc --rpcapi db,ccm,net,web3,personal,admin,miner --rpcport 7575 --rpcaddr 0.0.0.0 --rpccorsdomain "*" attach`

6. 在控制台内连接至ccm网络<br/>`admin.addPeer("enode://6157335ebf0e50f413dbb95d8238fa5e220b8dec4365b7bfdfb1a45d7de9dc5d9607f09390351f5878a9b298340c58ae55b4a1a8c97f97e163b23638e894bf1d@47.74.242.199:17575")
`
7. 输入命令admin.peers查看连接是否成功

8. 如需挖矿，请执行下面步骤

9. 在控制台内创建挖矿账户地址,例如:<br/>`personal.newAccount("密码")`

10. 在控制台内解锁挖矿地址,例如:<br/>`personal.unlockAccount("0xd22b5e568d813ac08de3d4c861fc601ccf8d9283",'密码', 0);`

11. 在控制台内启动挖矿<br/>`miner.start()`


### rpc连接
通过web3连接至ccm网络，需要修改web3第三方库源码：1.eth_为ccm_  2.chainId为10
