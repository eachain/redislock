# redislock

redislock依赖redis实现了一个简单的分布式锁。实现方式参考：http://doc.redisfans.com/string/set.html#id2。

文档中提到：

> 可以通过以下修改，让这个锁实现更健壮：
>
> - 不使用固定的字符串作为键的值，而是设置一个不可猜测（non-guessable）的长随机字符串，作为口令串（token）。
> - 不使用 DEL 命令来释放锁，而是发送一个 Lua 脚本，这个脚本只在客户端传入的值和键的口令串相匹配时，才对键进行删除。
>
> 这两个改动可以防止持有过期锁的客户端误删现有锁的情况出现。

本库除以上文档提到的点，还做了自动续租：当前持有锁临近过期时，本库实现的锁会自动续期，直到调用`Unlock`主动释放，保证锁的不可重入。



## 示例

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/eachain/redislock"
	"github.com/go-redis/redis/v8"
)

type redisClient redis.Client

func (r *redisClient) SetNX(ctx context.Context, key string, value any, expireSeconds int64) (bool, error) {
	return (*redis.Client)(r).SetNX(ctx, key, value,
		time.Duration(expireSeconds)*time.Second).Result()
}

func (r *redisClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return (*redis.Client)(r).Eval(ctx, script, keys, args...).Result()
}

func main() {
	var rdb *redis.Client

	mut := redislock.NewMutex((*redisClient)(rdb), "mutex:biz:key")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := mut.TryLock(ctx, 3)
	if err != nil {
		fmt.Printf("try lock distributed mutex: %v\n", err)
		return
	}
	defer mut.Unlock()

	fmt.Println("do your biz here")
}
```



