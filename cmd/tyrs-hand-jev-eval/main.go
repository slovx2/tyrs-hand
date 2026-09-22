// tyrs-hand-jev-eval 独立评测 Jev，不连接 Control 数据库或改动 Live。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/slovx2/tyrs-hand/internal/livejudge"
	"github.com/spf13/viper"
)

type frozen struct {
	Policy      string               `json:"policy"`
	Model       string               `json:"model"`
	Prompt      string               `json:"prompt"`
	PromptHash  string               `json:"prompt_hash"`
	DatasetHash string               `json:"dataset_hash"`
	Thresholds  livejudge.Thresholds `json:"thresholds"`
	CreatedAt   time.Time            `json:"created_at"`
}

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "develop", "develop、holdout 或 dataset")
	out := flag.String("out", ".local/jev-eval", "结果目录")
	env := flag.String("env", "", "指定 dotenv 文件，不回显配置")
	workers := flag.Int("concurrency", 3, "并行场景数；每个场景内部串行")
	policy := flag.String("policy", "strict", "strict 或 notification；后者优先在精确率达标后提高召回")
	flag.Parse()
	if *mode != "develop" && *mode != "holdout" && *mode != "dataset" {
		return fmt.Errorf("无效 mode")
	}
	if *policy != "strict" && *policy != "notification" {
		return fmt.Errorf("无效 policy")
	}
	if *workers < 1 || *workers > 8 {
		return fmt.Errorf("concurrency 必须在 1 到 8")
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	data := livejudge.EncodeDataset()
	if *mode == "dataset" {
		return os.WriteFile(filepath.Join(*out, "dataset.json"), data, 0600)
	}
	key, err := readAPIKey(*env)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("缺少 JEV_API_KEY；可通过 -env 指定文件")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var scenarios []livejudge.Scenario
	split := "dev"
	if *mode == "holdout" {
		split = "holdout"
	}
	for _, s := range livejudge.Dataset() {
		if s.Split == split {
			scenarios = append(scenarios, s)
		}
	}
	if err := os.WriteFile(filepath.Join(*out, "dataset.json"), data, 0600); err != nil {
		return err
	}
	var runs []livejudge.Run
	write := func(name string, value any) error {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(*out, name), b, 0600)
	}
	execute := func(prompt string, t livejudge.Thresholds, kind string, round int) livejudge.Run {
		client := livejudge.NewClient(key, prompt)
		rows := livejudge.Replay(ctx, scenarios, client, t, kind, *workers, func(id string) { fmt.Printf("%s %s/%d %s 完成\n", prompt, kind, round, id) })
		return livejudge.Run{Policy: *policy, Prompt: prompt, Thresholds: t, Mode: kind, Round: round, Rows: rows, Metrics: livejudge.Summarize(rows)}
	}
	var config frozen
	if *mode == "develop" {
		if _, err := os.Stat(filepath.Join(*out, "frozen.json")); err == nil {
			return fmt.Errorf("目录已有冻结配置，请使用新目录保存新的开发实验")
		}
		var best livejudge.Metrics
		selected := false
		for _, prompt := range livejudge.PromptVersions {
			single := execute(prompt, livejudge.Thresholds{Related: .5, Notify: .5, Completed: .5, NeedsInput: .5}, "single", 1)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			t, _ := livejudge.Tune(single.Rows)
			if *policy == "notification" {
				t, _ = livejudge.TuneNotification(single.Rows)
			}
			single.Thresholds = t
			single.Rows = livejudge.Reclassify(single.Rows, t)
			single.Metrics = livejudge.Summarize(single.Rows)
			loop := execute(prompt, t, "loop", 1)
			runs = append(runs, single, loop)
			if err := write("develop.json", runs); err != nil {
				return err
			}
			better := livejudge.Better(loop.Metrics, best)
			if *policy == "notification" {
				better = livejudge.BetterNotification(loop.Metrics, best)
			}
			if !selected || better {
				q, _ := livejudge.Questions(prompt)
				b, _ := json.Marshal(q)
				config = frozen{Policy: *policy, Model: livejudge.Model, Prompt: prompt, PromptHash: hash(b), DatasetHash: hash(data), Thresholds: t, CreatedAt: time.Now().UTC()}
				best = loop.Metrics
				selected = true
			}
			fmt.Println(prompt, "single", single.Metrics.Summary())
			fmt.Println(prompt, "loop", loop.Metrics.Summary())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := write("frozen.json", config); err != nil {
			return err
		}
	} else {
		b, err := os.ReadFile(filepath.Join(*out, "frozen.json"))
		if err != nil {
			return err
		}
		if err = json.Unmarshal(b, &config); err != nil {
			return err
		}
		*policy = config.Policy
		q, err := livejudge.Questions(config.Prompt)
		if err != nil {
			return err
		}
		qb, _ := json.Marshal(q)
		if config.Model != livejudge.Model || config.PromptHash != hash(qb) || config.DatasetHash != hash(data) {
			return fmt.Errorf("冻结后的模型、提示词或数据集发生变化，必须重新建立开发/留出实验")
		}
		if _, err := os.Stat(filepath.Join(*out, "holdout.json")); err == nil {
			return fmt.Errorf("留出集已有结果，禁止覆盖；修订后必须建立新留出样例")
		}
		for round := 1; round <= 3; round++ {
			for _, kind := range []string{"single", "loop"} {
				r := execute(config.Prompt, config.Thresholds, kind, round)
				runs = append(runs, r)
				if err := write("holdout.json", runs); err != nil {
					return err
				}
				fmt.Println(kind, round, r.Metrics.Summary())
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(*out, *mode+".md"), []byte(livejudge.Markdown(runs)), 0600); err != nil {
		return err
	}
	distributions := livejudge.ScoreDistributions(runs)
	if err := write(*mode+"-scores.json", distributions); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, *mode+"-scores.md"), []byte(livejudge.DistributionMarkdown(distributions)), 0600); err != nil {
		return err
	}
	if *mode == "holdout" {
		for _, r := range runs {
			if !r.Metrics.Passed() {
				return fmt.Errorf("留出集未通过验收，完整报告已保存；保持现有 Live")
			}
		}
	}
	return nil
}

func readAPIKey(envPath string) (string, error) {
	key := os.Getenv("JEV_API_KEY")
	if envPath != "" {
		v := viper.New()
		v.SetConfigFile(envPath)
		v.SetConfigType("env")
		if err := v.ReadInConfig(); err != nil {
			return "", fmt.Errorf("无法读取指定 env 文件")
		}
		if value := v.GetString("JEV_API_KEY"); value != "" {
			key = value
		}
	}
	return key, nil
}
