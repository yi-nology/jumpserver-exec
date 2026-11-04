package arch_test

// 执行面（jumpserver-mcp-exec）自己的架构守卫薄壳。
//
// ⚠️ 这里只有**判据**：本仓是哪个仓、它的目录长什么样、它的契约词汇叫什么、
// 它的凭据参数有哪些、它不许有什么。守卫的**体**在共用底座
// jumpserver-mcp-kit 的 pkg/arch 里（两个服务仓的守卫体也在那儿）——
// 抄三份必然漂，而漂的那份决定守卫认不认得出破契约。
//
// ⚠️ 与拆分前的单仓相比，判据的形状变了三处：
//
//   - module path 来自 kit 的常量（**唯一**一份），不再由本仓自己写一份；
//   - 「两个服务」的那些守卫拆成了每个仓自己的一条——**对方是谁**仍然是
//     本仓的判据（谁不许被 import / 被 require），但扫的是**本仓**这棵树；
//   - 契约词汇的目录不再由固定的路径算术推出来，而是**点名**（hz 的布局里
//     服务的模型在 biz/<service>/model/，共用的在 model/，没有一个统一的
//     路径算术了——但**名字**仍然得只说一件事）。
//
// ⚠️ 两条守卫是**本仓自己的**（kit 那边没有对应的体）：入口层接上留痕，
// 以及「答复集合是机械地扫出来的」——它们在本文件末尾。
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/jumpserver-mcp-kit/pkg/arch"
)

// 本仓的判据。
var (
	// handlerDirs 是工具实现所在的目录：答复从那里交出来，参数也从那里来。
	handlerDirs = []string{"biz/handler"}

	// contractModels 是**生成契约模型**的目录（thriftgo 的产物）。
	//
	// ⚠️ 一个词汇按 IDL 域拆成了若干文件，这里说的是它们**住在哪**。
	contractModels = []string{"biz/model/jmsexec"}

	// answerSources 是工具**声明答复形状**的目录：答复类型是从这里扫出来的
	// （每个工具在它的 handler 里声明 render.Schema[...]）。
	//
	// ⚠️ 它与 handlerDirs 同源但不是同一件事：前者是「答复从哪儿交出来」，
	// 后者是「答复的形状从哪儿**声明**出来」。
	answerSources = []string{"biz/handler"}

	// credentialParameters 是本仓真的当成凭据的参数名。
	//
	// ⚠️ **空表是事实，不是漏抄**：执行面的工具不接任何凭据参数（它拿的是
	// 会话里已有的东西），所以脱敏器在本仓的参数上**一个也不该**命中。
	// ⚠️ 表若是空的，那条「不许腐烂」的断言对本仓不适用——底座那边会容忍
	// 空表（遍历一张空表就是什么都不检查）。
	credentialParameters = map[string]string{}

	// errorBearingAnswers 是「允许带 Error 字段」的答复清单。
	//
	// ⚠️ 它是**一份清单**，不是一条规则：Error 字段是留痕**按名字**读的，
	// 谁给一个答复加上 Error 字段，就是改了留痕对那个工具的说法——那得是
	// 一个**故意**的动作，不是一个巧合。
	//
	// ⚠️ 每条后面的话是**这个失败意味着什么**：留痕的 failed 列要能让人
	// 读出「这活没干成」还是「这只是诊断没成」。执行面这一侧的失败
	// 往往意味着**命令可能还在那台机器上跑着**。
	errorBearingAnswers = map[string]string{
		"jmsexec.ReachableResult":    "the probe could not reach the asset",
		"jmsexec.BatchResult":        "some commands in the batch did not work",
		"jmsexec.CollectResult":      "some checks on the asset did not work",
		"jmsexec.ExecMultiResult":    "some hosts in the sweep did not work",
		"jmsexec.CollectMultiResult": "some hosts in the sweep did not work",
	}

	// versionPlanes 说本仓的配置目录在哪（按面说，不按目录名猜）。
	versionPlanes = map[string]string{"execution plane": "internal/config"}

	// versionSettings 是本仓读的版本设置。
	//
	// ⚠️ 它是**列出来**的，不是推出来的：这条守卫的点就在于这些设置
	// **存在**；从代码推出它们只能说明代码与它自己一致。
	versionSettings = []string{
		"JUMPSERVER_VERSION",
		"JUMPSERVER_USERNAME",
		"JUMPSERVER_PASSWORD",
		"JUMPSERVER_OTP",
		"JUMPSERVER_TOKEN",
	}
)

// executionRoot 返回执行面仓的根。
//
// ⚠️ 它从**测试自己的位置**推出来（internal/arch → 上两级），而不是从
// 工作目录推出来 ⇒ 从哪儿启动测试都不影响。
func executionRoot(t *testing.T) string {
	t.Helper()
	root, err := arch.RepoRoot(".")
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	return root
}

// executionTree 是执行面这棵树（根 + module path + 人话名字）。
func executionTree(t *testing.T) arch.Tree {
	t.Helper()
	return arch.Tree{
		Root:       executionRoot(t),
		ModulePath: arch.ModuleExecution,
		Name:       "execution",
	}
}

// TestNoBareGoroutines 守住 oekit 矩阵的防 panic 规则。
func TestNoBareGoroutines(t *testing.T) {
	arch.CheckNoBareGoroutines(t, executionRoot(t))
}

// TestTheBinariesAreTheOutermostLayer 让入口层不要变成一个别人依赖的库。
func TestTheBinariesAreTheOutermostLayer(t *testing.T) {
	tree := executionTree(t)
	arch.CheckBinariesAreOutermost(t, tree.Root, tree.ModulePath)
}

// TestTheEntryLayerIsOneBinaryAtTheRoot 让「一个仓一个二进制」这件事是
// 机械地成立的。
//
// ⚠️ 入口层搬到仓根之后，二进制就在仓根 ⇒ 子命令必须是**入口层 import
// 的包**，不是各自的二进制 ⇒ 否则仓里就有了第二个入口，而发版只发一个。
func TestTheEntryLayerIsOneBinaryAtTheRoot(t *testing.T) {
	tree := executionTree(t)
	arch.CheckTheEntryLayerIsOneBinaryAtTheRoot(t, tree.Root, tree.ModulePath)
}

// TestTheContractVocabularyIsNamedTheSameEverywhere 让 IDL、生成包与模型
// 目录始终同名。
func TestTheContractVocabularyIsNamedTheSameEverywhere(t *testing.T) {
	tree := executionTree(t)
	arch.CheckContractVocabularyNaming(t, tree.Root, tree.ModulePath)
}

// TestThisServiceDoesNotImportTheOtherService 守住两个服务之间的边界。
//
// ⚠️ 拆仓之后编译器不再替我们守这条线（两个服务是**两个 module**），
// 而这条边界恰恰是最容易被「就 import 一下」越过去的。
func TestThisServiceDoesNotImportTheOtherService(t *testing.T) {
	tree := executionTree(t)
	arch.CheckNoForbiddenImport(t, tree.Root, tree.ModulePath,
		[]string{arch.ModuleManagement})
}

// TestThisServiceDoesNotRequireTheOtherService 让 go.mod 也不许承诺对方。
//
// ⚠️ 源码与 go.mod 两头都要查：源码查「真的伸手了没有」，go.mod 查
// 「承诺了没有」——依赖图上写着「我需要它」，而 require 里的版本号
// 本身就是一个**承诺**。
func TestThisServiceDoesNotRequireTheOtherService(t *testing.T) {
	tree := executionTree(t)
	arch.CheckNoForbiddenRequire(t, tree.Root, tree.ModulePath,
		[]string{arch.ModuleManagement})
}

// TestToolAnswersAreContractTypes 让每个工具都守这条规矩：答复是契约类型，
// 不是手敲出来的 map。
func TestToolAnswersAreContractTypes(t *testing.T) {
	arch.CheckToolAnswersAreContractTypes(t, executionRoot(t), handlerDirs)
}

// TestTheRepositorysOwnParametersAreNotMistakenForCredentials 用仓自己的
// 参数表校住脱敏器的词表。
//
// ⚠️ 本仓的表是**空的**（执行面没有凭据参数）——空表是事实，不是漏抄。
func TestTheRepositorysOwnParametersAreNotMistakenForCredentials(t *testing.T) {
	arch.CheckCredentialParameters(t, executionRoot(t), handlerDirs,
		credentialParameters)
}

// TestAContractThatCanRehearseCanAlsoRefuse 钉住判定约定。
func TestAContractThatCanRehearseCanAlsoRefuse(t *testing.T) {
	arch.CheckVerdictConvention(t, executionRoot(t), contractModels,
		answerSources, errorBearingAnswers)
}

// TestThisPlaneCarriesTheVersionSetting 让本面认得出部署是什么、怎么到它。
func TestThisPlaneCarriesTheVersionSetting(t *testing.T) {
	arch.CheckVersionSettings(t, executionRoot(t), versionPlanes, versionSettings)
}

// TestTheCapabilityVocabulary 守住能力模型的词汇，以及它带来的门禁。
//
// ⚠️ 这一条在**每个**服务仓都要跑：委派调用的**调用点**在服务仓里
// （kit 的 pkg/jmsapi 里只有方案自己的 API）。
func TestTheCapabilityVocabulary(t *testing.T) {
	tree := executionTree(t)
	arch.CheckCapabilityVocabulary(t, tree.Root, tree.ModulePath)
}

// TestTheVersionDoesNotLeaveTheProtocolLayer 让发行版留在控制流之外。
func TestTheVersionDoesNotLeaveTheProtocolLayer(t *testing.T) {
	tree := executionTree(t)
	arch.CheckVersionDoesNotLeaveProtocolLayer(t, tree.Root, tree.ModulePath)
}

// TestTheDependenciesArePinned 让依赖的接线保持同步。
//
// ⚠️ 底座 kit 还没发布 ⇒ 本仓还没有 go.sum（依赖的内容哈希由**不入库**的
// go.work.sum 顶着），底座本身也不在 module cache 里（它由工作集提供，
// 恢复脚本的 in_workspace 正是为此）⇒ 「每个依赖都被 go.sum 钉住、都
// 已经在 cache 里解包」这件事现在**没有可判的对象**：不是它不成立，
// 而是它还没成立到能判。
//
// ⚠️ 跳过 ≠ 通过：JMS_REQUIRE_PUBLISHED=1 ⇒ 这里**失败**而不是跳过。
func TestTheDependenciesArePinned(t *testing.T) {
	root := executionRoot(t)
	if _, err := os.Stat(filepath.Join(root, "go.sum")); err != nil {
		msg := "this repository has no go.sum yet: its dependencies resolve through the " +
			"local workspace (go.work, not committed) while the shared base " +
			"(jumpserver-mcp-kit) is still unpublished, so there is nothing pinned to " +
			"check yet. Once the base is published and this repository gains a go.sum, " +
			"this guard starts running by itself."
		if os.Getenv("JMS_REQUIRE_PUBLISHED") == "1" {
			t.Fatal(msg + " JMS_REQUIRE_PUBLISHED=1 is set, so this is a FAILURE, not a skip.")
		}
		t.Skip(msg + " Set JMS_REQUIRE_PUBLISHED=1 to turn this skip into a failure.")
	}
	arch.CheckDependencyPinning(t, root, "script/restore-go-modcache.sh")
}

// TestThisServiceKeepsTheToolCallTrail makes sure this service's entry point
// wires the audit trail.
//
// It matters more for the execution plane than for the management one: a
// DANGEROUS command refused for want of a confirmation never reaches the
// bastion, so the bastion has no session to show for it. Without the local
// trail, "who tried to run rm -rf on that box and was stopped" has no answer
// anywhere — the refusal leaves no trace at all.
//
// The check is textual on purpose: what it protects is a wiring decision in
// the entry point, and the shared implementation is what keeps the servers
// logging the same fields.
func TestThisServiceKeepsTheToolCallTrail(t *testing.T) {
	root := executionRoot(t)
	// ⚠️ 留痕接在**跑服务那条路**上（cmd/serve/），而它不在仓根：
	// 仓根那个 package 是 main，Go 不许 import 一个 main 包 ⇒ 子命令的
	// 实现只能住在 cmd/ 下。
	entry := filepath.Join(root, "cmd/serve/serve.go")
	body, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("read %s: %v", entry, err)
	}
	if !strings.Contains(string(body), "audit.Trail(") {
		t.Errorf("%s does not wire the shared tool-call trail: "+
			"a refused tool call leaves no record anywhere", arch.Relative(root, entry))
	}
}

// TestTheAnswerTypesAreFoundMechanically keeps the answer set honest.
//
// The rules that speak of an answer are only as good as the set they are
// applied to. The set comes from scanning the handlers, and this test says
// what that scan must find: this plane's rehearsal answers (they carry the
// verdict fields), an answer that can only REFUSE (it must not slip
// through), and the trail's own row type (it is not an answer — it is the
// trail's shape, and it carries `refused` instead of `blocked`).
func TestTheAnswerTypesAreFoundMechanically(t *testing.T) {
	answers := arch.AnswerTypes(t, executionRoot(t), contractModels, answerSources)
	for _, key := range []string{
		"jmsexec.CommandResult",
		"jmsexec.CollectResult",
		"jmsexec.ExecMultiResult",
		"jmsexec.BgStartResult",
	} {
		if !answers[key] {
			t.Errorf("%s is a tool's answer but the scan did not find it", key)
		}
	}
	// The trail's own row type is the shape of a trail line, not an answer.
	if answers["jmsexec.AuditEntry"] {
		t.Error("the trail's own row type is not an answer; the scan must not claim it")
	}
}
