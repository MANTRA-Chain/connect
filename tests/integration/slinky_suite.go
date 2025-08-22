package integration

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/cosmos"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	oracleconfig "github.com/skip-mev/connect/v2/oracle/config"
	connecttypes "github.com/skip-mev/connect/v2/pkg/types"
	"github.com/skip-mev/connect/v2/providers/apis/marketmap"
	mmtypes "github.com/skip-mev/connect/v2/x/marketmap/types"
	oracletypes "github.com/skip-mev/connect/v2/x/oracle/types"
)

const (
	envKeepAlive          = "ORACLE_INTEGRATION_KEEPALIVE"
	genesisAmount         = 1000000000
	defaultDenom          = "stake"
	validatorKey          = "validator"
	yes                   = "yes"
	deposit               = 1000000
	userMnemonic          = "foster poverty abstract scorpion short shrimp tilt edge romance adapt only benefit moral another where host egg echo ability wisdom lizard lazy pool roast"
	userAccountAddressHex = "70990fdcf97778b6fef25915ea6b2da94d131208"
	gasPrice              = 100
)

func DefaultOracleSidecar(image ibc.DockerImage) ibc.SidecarConfig {
	return ibc.SidecarConfig{
		ProcessName: "oracle",
		Image:       image,
		HomeDir:     "/oracle",
		Ports:       []string{"8080", "8081"},
		StartCmd: []string{
			"connect",
			"--oracle-config", "/oracle/oracle.json",
		},
		ValidatorProcess: true,
		PreStart:         true,
	}
}

func DefaultOracleConfig(url string) oracleconfig.OracleConfig {
	cfg := marketmap.DefaultAPIConfig
	cfg.Endpoints = []oracleconfig.Endpoint{
		{
			URL: url,
		},
	}

	// Create the oracle config
	oracleConfig := oracleconfig.OracleConfig{
		UpdateInterval: 500 * time.Millisecond,
		MaxPriceAge:    1 * time.Minute,
		Host:           "0.0.0.0",
		Port:           "8080",
		Providers: map[string]oracleconfig.ProviderConfig{
			marketmap.Name: {
				Name: marketmap.Name,
				API:  cfg,
				Type: "market_map_provider",
			},
		},
	}

	return oracleConfig
}

func DefaultMarketMap() mmtypes.MarketMap {
	return mmtypes.MarketMap{}
}

func GetOracleSideCar(node *cosmos.ChainNode) *cosmos.SidecarProcess {
	if len(node.Sidecars) == 0 {
		panic("no sidecars found")
	}
	return node.Sidecars[0]
}

type ConnectIntegrationSuite struct {
	suite.Suite

	spec *interchaintest.ChainSpec

	// add more fields here as necessary
	chain *cosmos.CosmosChain

	// oracle side-car config
	oracleConfig ibc.SidecarConfig

	// user
	user cosmos.User

	// default token denom
	denom string

	// authority address
	authority sdk.AccAddress

	// block time
	blockTime time.Duration

	// interchain constructor
	icc InterchainConstructor

	// interchain
	ic Interchain

	// chain constructor
	cc ChainConstructor
}

// Option is a function that modifies the ConnectIntegrationSuite
type Option func(*ConnectIntegrationSuite)

// WithDenom sets the token denom
func WithDenom(denom string) Option {
	return func(s *ConnectIntegrationSuite) {
		s.denom = denom
	}
}

// WithAuthority sets the authority address
func WithAuthority(addr sdk.AccAddress) Option {
	return func(s *ConnectIntegrationSuite) {
		s.authority = addr
	}
}

// WithBlockTime sets the block time
func WithBlockTime(t time.Duration) Option {
	return func(s *ConnectIntegrationSuite) {
		s.blockTime = t
	}
}

// WithInterchainConstructor sets the interchain constructor
func WithInterchainConstructor(ic InterchainConstructor) Option {
	return func(s *ConnectIntegrationSuite) {
		s.icc = ic
	}
}

// WithChainConstructor sets the chain constructor
func WithChainConstructor(cc ChainConstructor) Option {
	return func(s *ConnectIntegrationSuite) {
		s.cc = cc
	}
}

// CreateTx creates a new transaction to be signed by the given user, including a provided set of messages
func CreateTx(t *testing.T, chain *cosmos.CosmosChain, user cosmos.User, GasPrice int64, msgs ...sdk.Msg) []byte {
	bc := cosmos.NewBroadcaster(t, chain)

	ctx := context.Background()
	// create tx factory + Client Context
	txf, err := bc.GetFactory(ctx, user)
	require.NoError(t, err)

	cc, err := bc.GetClientContext(ctx, user)
	require.NoError(t, err)

	txf = txf.WithSimulateAndExecute(true)

	txf, err = txf.Prepare(cc)
	require.NoError(t, err)

	// get gas for tx
	txf.WithGas(25000000)

	// update sequence number
	txf = txf.WithSequence(txf.Sequence())
	txf = txf.WithGasPrices(sdk.NewDecCoins(sdk.NewDecCoin(chain.Config().Denom, math.NewInt(GasPrice))).String())

	// sign the tx
	txBuilder, err := txf.BuildUnsignedTx(msgs...)
	require.NoError(t, err)

	require.NoError(t, tx.Sign(cc.CmdContext, txf, cc.GetFromName(), txBuilder, true))

	// encode and return
	bz, err := cc.TxConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)
	return bz
}

func NewConnectIntegrationSuite(spec *interchaintest.ChainSpec, oracleImage ibc.DockerImage, opts ...Option) *ConnectIntegrationSuite {
	suite := &ConnectIntegrationSuite{
		spec:         spec,
		oracleConfig: DefaultOracleSidecar(oracleImage),
		denom:        defaultDenom,
		authority:    authtypes.NewModuleAddress(govtypes.ModuleName),
		blockTime:    10 * time.Second,
		icc:          DefaultInterchainConstructor,
		cc:           DefaultChainConstructor,
	}

	for _, opt := range opts {
		opt(suite)
	}

	return suite
}

func (s *ConnectIntegrationSuite) SetupSuite() {
	// update market-map params to add the user as the market-authority
	accountAddressBz, err := hex.DecodeString(userAccountAddressHex)
	if err != nil {
		panic(err)
	}
	accountAddress, err := bech32.ConvertAndEncode(s.spec.ChainConfig.Bech32Prefix, accountAddressBz)
	fmt.Println("mm-accountAddress", s.spec.ChainConfig.Bech32Prefix, accountAddress, err)
	if err != nil {
		panic(err)
	}
	existingGenesisModifier := s.spec.ChainConfig.ModifyGenesis
	s.spec.ChainConfig.ModifyGenesis = func(cc ibc.ChainConfig, genesisBz []byte) ([]byte, error) {
		genesisBz, err := cosmos.ModifyGenesis([]cosmos.GenesisKV{
			cosmos.NewGenesisKV(
				"app_state.marketmap.params.admin",
				accountAddress,
			),
			cosmos.NewGenesisKV(
				"app_state.marketmap.params.market_authorities.0",
				accountAddress,
			),
		})(cc, genesisBz)
		if err != nil {
			return nil, err
		}

		return existingGenesisModifier(cc, genesisBz)
	}

	chains := s.cc(s.T(), s.spec)

	if len(chains) < 1 {
		panic("no chains created")
	}

	chains[0].WithPreStartNodes(func(c *cosmos.CosmosChain) {
		// for each node in the chain, set the sidecars
		for i := range c.Nodes() {
			// pin
			node := c.Nodes()[i]
			// add sidecars to node
			AddSidecarToNode(node, s.oracleConfig)

			// set config for the oracle
			oracleCfg := DefaultOracleConfig("localhost:9090")
			SetOracleConfigsOnOracle(GetOracleSideCar(node), oracleCfg)

			// set the out-of-process oracle config for all nodes
			node.WithPreStartNode(func(n *cosmos.ChainNode) {
				SetOracleConfigsOnApp(n)
			})
		}
	})

	// start the chain
	s.ic = s.icc(context.Background(), s.T(), chains)
	s.chain = chains[0]
	s.user, err = interchaintest.GetAndFundTestUserWithMnemonic(context.Background(), s.T().Name(), userMnemonic, math.NewInt(genesisAmount), s.chain)
	s.Require().NoError(err)
}

func (s *ConnectIntegrationSuite) TearDownSuite() {
	defer s.Teardown()
	// get the oracle integration-test suite keep alive env
	if ok := os.Getenv(envKeepAlive); ok == "" {
		return
	}

	// await on a signal to keep the chain running
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s.T().Log("Keeping the chain running")
	<-sig
}

func (s *ConnectIntegrationSuite) Teardown() {
	// stop all nodes + sidecars in the chain
	ctx := context.Background()
	if s.chain == nil {
		return
	}

	s.chain.StopAllNodes(ctx)
	s.chain.StopAllSidecars(ctx)

	// if there is a provider, stop that as well
	if s.chain.Provider != nil {
		s.chain.Provider.StopAllNodes(ctx)
		s.chain.Provider.StopAllSidecars(ctx)
	}
}

func (s *ConnectIntegrationSuite) SetupTest() {
	s.TearDownSuite()
	s.SetupSuite()

	// reset the oracle services
	// start all oracles
	for _, node := range s.chain.Nodes() {
		oCfg := DefaultOracleConfig(translateGRPCAddr(s.chain))

		SetOracleConfigsOnOracle(GetOracleSideCar(node), oCfg)
		s.Require().NoError(RestartOracle(node))
	}
}

type SlinkyOracleIntegrationSuite struct {
	*ConnectIntegrationSuite
}

func NewSlinkyOracleIntegrationSuite(suite *ConnectIntegrationSuite) *SlinkyOracleIntegrationSuite {
	return &SlinkyOracleIntegrationSuite{
		ConnectIntegrationSuite: suite,
	}
}

func translateGRPCAddr(chain *cosmos.CosmosChain) string {
	return chain.GetGRPCAddress()
}

func (s *SlinkyOracleIntegrationSuite) TestMultiplePriceFeeds() {
	ethusdcCP := connecttypes.NewCurrencyPair("ETH", "USDC")
	ethusdtCP := connecttypes.NewCurrencyPair("ETH", "USDT")
	ethusdCP := connecttypes.NewCurrencyPair("ETH", "USD")

	// add multiple currency pairs
	cps := []connecttypes.CurrencyPair{
		ethusdcCP,
		ethusdtCP,
		ethusdCP,
	}
	fmt.Println("mm-AddCurrencyPairs")
	s.Require().NoError(s.AddCurrencyPairs(s.chain, s.user, 1.1, cps...))
	fmt.Println("mm-AddCurrencyPairs-af")

}

func getIDForCurrencyPair(ctx context.Context, client oracletypes.QueryClient, cp connecttypes.CurrencyPair) (uint64, error) {
	// query for the given currency pair
	resp, err := client.GetPrice(ctx, &oracletypes.GetPriceRequest{
		CurrencyPair: cp.String(),
	})
	if err != nil {
		return 0, err
	}

	return resp.Id, nil
}
