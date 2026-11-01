// Package llm provides SiliconFlow chat-completions client for watch-session judgment.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultBaseURL = "https://api.siliconflow.cn/v1"
	defaultModel   = "deepseek-ai/DeepSeek-V4-Flash"
	maxReasonRunes = 80
)

// SystemPrompt is the fixed Chinese system instruction for entertainment vs productive judgment.
const SystemPrompt = `你是 TimeSavior 的观看内容审核助手。根据用户提供的近一段时间观看清单（标题、作者、标签、各时长），判断整体更接近：
1) 工作 / 学习 / 正经爱好 → 放行。包括：课程教程、专业技能、语言备考、运动教学；以及学术会议、学术主页、论文/会议官网、研讨会、workshop、CFP、proceedings 等。主页/频道页若整体是学术或专业内容，也放行。不要仅因「主页/推荐流形态」就判为奶头乐。
2) 无营养「奶头乐」娱乐刷屏（短视频猎奇、纯八卦、无目的刷推荐、低信息密度消遣等）→ 拦截。

只输出一个 JSON 对象，字段顺序必须是：先 reason，后 allow。
- reason：一句中文简述，不超过 80 字。
- allow：true 表示放行继续观看，false 表示应拦截。
不要输出其它文字、不要 markdown 代码块、不要思考过程。`

// Clip is one video observed in the watch window.
type Clip struct {
	Title       string   `json:"title"`
	Author      string   `json:"author,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	DurationSec float64  `json:"duration_sec"`
	VideoID     string   `json:"video_id,omitempty"`
}

// Verdict is the model decision.
type Verdict struct {
	Reason string `json:"reason"`
	Allow  bool   `json:"allow"`
}

// Client talks to SiliconFlow OpenAI-compatible APIs.
type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

func NewClient(baseURL, apiKey, model string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		model = defaultModel
	}
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		HTTPClient: &http.Client{Timeout: 45 * time.Second},
	}
}

// BalanceCNY reads account total balance via GET /user/info (currency CNY).
func (c *Client) BalanceCNY(ctx context.Context) (float64, error) {
	if c == nil || c.APIKey == "" {
		return 0, fmt.Errorf("llm: missing api key")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/user/info", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("llm: user/info HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var raw struct {
		Status bool `json:"status"`
		Data   struct {
			Balance       string `json:"balance"`
			TotalBalance  string `json:"totalBalance"`
			ChargeBalance string `json:"chargeBalance"`
		} `json:"data"`
		// some responses nest differently
		Balance      string `json:"balance"`
		TotalBalance string `json:"totalBalance"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("llm: user/info parse: %w", err)
	}
	candidates := []string{raw.Data.TotalBalance, raw.Data.Balance, raw.TotalBalance, raw.Balance, raw.Data.ChargeBalance}
	for _, s := range candidates {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(s, "%f", &v); err == nil {
			return v, nil
		}
	}
	return 0, fmt.Errorf("llm: user/info: no balance field")
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	MaxTokens      int           `json:"max_tokens"`
	EnableThinking bool          `json:"enable_thinking"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type respFormat struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// JudgeResult is the outcome of one Judge call including optional cost from balance delta.
type JudgeResult struct {
	Verdict       Verdict
	CostCNY       float64
	CostEstimated bool // true = token×price estimate (not balance delta)
	BalBefore     float64
	BalAfter      float64
	RawContent    string
}

// Judge asks the model whether to allow continued watching.
// When trackBalance is true, cost prefers /user/info balance delta; otherwise only token estimate.
func (c *Client) Judge(ctx context.Context, site string, windowMin int, clips []Clip, trackBalance bool) (JudgeResult, error) {
	var out JudgeResult
	if c == nil || c.APIKey == "" {
		return out, fmt.Errorf("llm: missing api key")
	}
	user := BuildUserPrompt(site, windowMin, clips)

	var balErr error
	if trackBalance {
		var balBefore float64
		balBefore, balErr = c.BalanceCNY(ctx)
		out.BalBefore = balBefore
	}

	payload := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: user},
		},
		Temperature:    0.2,
		MaxTokens:      256,
		EnableThinking: false, // Non-Think mode for DeepSeek-V4-Flash on SiliconFlow
		ResponseFormat: &respFormat{Type: "json_object"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("llm: chat HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return out, fmt.Errorf("llm: chat parse: %w", err)
	}
	if len(cr.Choices) == 0 {
		return out, fmt.Errorf("llm: empty choices")
	}
	content := strings.TrimSpace(cr.Choices[0].Message.Content)
	out.RawContent = content
	v, err := ParseVerdict(content)
	if err != nil {
		return out, err
	}
	out.Verdict = v

	if trackBalance && balErr == nil {
		if balAfter, err2 := c.BalanceCNY(ctx); err2 == nil {
			out.BalAfter = balAfter
			if out.BalBefore >= balAfter {
				out.CostCNY = out.BalBefore - balAfter
			}
		}
	}
	// Token×price estimate when balance delta unavailable / zero (or cost monitor off).
	if out.CostCNY <= 0 && (cr.Usage.PromptTokens > 0 || cr.Usage.CompletionTokens > 0) {
		// ¥1.0/M in + ¥2.0/M out (approx $0.14/$0.28 × 7.2)
		out.CostCNY = float64(cr.Usage.PromptTokens)/1e6*1.0 + float64(cr.Usage.CompletionTokens)/1e6*2.0
		out.CostEstimated = true
	}
	return out, nil
}

// ShortTitleSystem is the fixed system instruction for title shortening.
const ShortTitleSystem = `将网页或应用标题压缩成单行中文短名。保留核心含义，严格不超过用户指定的字数（按显示字符/rune计）。只输出短名本身，不要引号、不要解释、不要思考过程。`

// ShortenResult is the outcome of one ShortenTitle call.
type ShortenResult struct {
	Short      string
	CostCNY    float64
	RawContent string
}

// BuildShortTitleUserPrompt formats the user message for ShortenTitle.
func BuildShortTitleUserPrompt(title string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 23
	}
	return fmt.Sprintf("限≤%d字（按字符计，含标点）。标题：%s", maxRunes, strings.TrimSpace(title))
}

// FormatMessagesRequest renders system+user as the request transcript stored in llm_dialogs.
func FormatMessagesRequest(system, user string) string {
	var b strings.Builder
	b.WriteString("[system]\n")
	b.WriteString(system)
	b.WriteString("\n\n[user]\n")
	b.WriteString(user)
	return b.String()
}

// ShortenTitle asks the model for a one-line short label (thinking disabled).
// Returns an error if the cleaned result exceeds maxRunes (no silent truncate).
func (c *Client) ShortenTitle(ctx context.Context, title string, maxRunes int) (ShortenResult, error) {
	var out ShortenResult
	if c == nil || c.APIKey == "" {
		return out, fmt.Errorf("llm: missing api key")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return out, fmt.Errorf("llm: empty title")
	}
	if maxRunes <= 0 {
		maxRunes = 23
	}
	user := BuildShortTitleUserPrompt(title, maxRunes)
	payload := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: ShortTitleSystem},
			{Role: "user", Content: user},
		},
		Temperature:    0.2,
		MaxTokens:      64,
		EnableThinking: false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("llm: chat HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return out, fmt.Errorf("llm: chat parse: %w", err)
	}
	if len(cr.Choices) == 0 {
		return out, fmt.Errorf("llm: empty choices")
	}
	content := strings.TrimSpace(cr.Choices[0].Message.Content)
	out.RawContent = content
	short := content
	short = strings.Trim(short, "「」\"'`")
	short = strings.ReplaceAll(short, "\n", " ")
	short = strings.TrimSpace(short)
	if short == "" {
		return out, fmt.Errorf("llm: empty short title")
	}
	if n := utf8.RuneCountInString(short); n > maxRunes {
		return out, fmt.Errorf("llm: short title too long (%d > %d)", n, maxRunes)
	}
	out.Short = short
	if cr.Usage.PromptTokens > 0 || cr.Usage.CompletionTokens > 0 {
		out.CostCNY = float64(cr.Usage.PromptTokens)/1e6*1.0 + float64(cr.Usage.CompletionTokens)/1e6*2.0
	}
	return out, nil
}

// BuildUserPrompt formats the clip list for the model.
func BuildUserPrompt(site string, windowMin int, clips []Clip) string {
	var b strings.Builder
	fmt.Fprintf(&b, "站点：%s\n连续观看窗口：约 %d 分钟\n观看清单：\n", site, windowMin)
	if len(clips) == 0 {
		b.WriteString("（窗口内未采集到具体标题，请按偏娱乐刷屏保守判断）\n")
		return b.String()
	}
	for i, c := range clips {
		tags := strings.Join(c.Tags, ",")
		fmt.Fprintf(&b, "%d. 标题=%s | 作者=%s | 标签=%s | 时长约%.0f秒",
			i+1, emptyDash(c.Title), emptyDash(c.Author), emptyDash(tags), c.DurationSec)
		if c.VideoID != "" {
			fmt.Fprintf(&b, " | id=%s", c.VideoID)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

var (
	reJSONObject = regexp.MustCompile(`(?s)\{.*\}`)
)

// ParseVerdict extracts allow/reason JSON from model output.
func ParseVerdict(content string) (Verdict, error) {
	var v Verdict
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		m := reJSONObject.FindString(s)
		if m == "" {
			return v, fmt.Errorf("llm: no JSON in response: %s", truncate(s, 120))
		}
		if err2 := json.Unmarshal([]byte(m), &v); err2 != nil {
			return v, fmt.Errorf("llm: verdict JSON: %w", err2)
		}
	}
	v.Reason = truncateRunes(strings.TrimSpace(v.Reason), maxReasonRunes)
	if v.Reason == "" {
		v.Reason = "模型未给出理由"
	}
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
