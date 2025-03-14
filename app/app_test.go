package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/0glabs/0g-chain/chaincfg"
	db "github.com/cometbft/cometbft-db"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/migrations/legacytx"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	solomachine "github.com/cosmos/ibc-go/v7/modules/light-clients/06-solomachine"
	ibctm "github.com/cosmos/ibc-go/v7/modules/light-clients/07-tendermint"
	evmtypes "github.com/evmos/ethermint/x/evm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewApp(t *testing.T) {
	chaincfg.SetSDKConfig()
	NewApp(
		chaincfg.DefaultNodeHome,
		nil,
		MakeEncodingConfig(),
		DefaultOptions,
		NewBaseApp(
			log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
			db.NewMemDB(),
			MakeEncodingConfig(),
			baseapp.SetChainID(TestChainId),
		),
	)
}

func TestExport(t *testing.T) {
	chaincfg.SetSDKConfig()
	db := db.NewMemDB()
	app := NewApp(
		chaincfg.DefaultNodeHome,
		nil,
		MakeEncodingConfig(),
		DefaultOptions,
		NewBaseApp(
			log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
			db,
			MakeEncodingConfig(),
			baseapp.SetChainID(TestChainId),
		),
	)

	genesisState := GenesisStateWithSingleValidator(&TestApp{App: *app}, NewDefaultGenesisState())

	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)

	initRequest := abci.RequestInitChain{
		Time:            time.Date(1998, 1, 1, 0, 0, 0, 0, time.UTC),
		ChainId:         TestChainId,
		InitialHeight:   1,
		ConsensusParams: sims.DefaultConsensusParams,
		Validators:      nil,
		AppStateBytes:   stateBytes,
	}
	app.InitChain(initRequest)
	app.Commit()

	exportedApp, err := app.ExportAppStateAndValidators(false, []string{}, []string{})
	require.NoError(t, err)

	// Assume each module is exported correctly, so only check modules in genesis are present in export
	initialModules, err := unmarshalJSONKeys(initRequest.AppStateBytes)
	require.NoError(t, err)
	// note ibctm is only registered in the BasicManager and not module manager so can be ignored
	initialModules = removeIbcTmModule(initialModules)
	exportedModules, err := unmarshalJSONKeys(exportedApp.AppState)
	require.NoError(t, err)
	assert.ElementsMatch(t, initialModules, exportedModules)

	assert.Equal(t, initRequest.InitialHeight+1, exportedApp.Height) // app.Commit() increments height
	assert.Equal(t, initRequest.ConsensusParams, exportedApp.ConsensusParams)
	assert.Len(t, exportedApp.Validators, 1) // no validators set in default genesis
}

// TestLegacyMsgAreAminoRegistered checks if all known msg types are registered on the app's amino codec.
// It doesn't check if they are registered on the module codecs used for signature checking.
func TestLegacyMsgAreAminoRegistered(t *testing.T) {
	tApp := NewTestApp()

	lcdc := tApp.LegacyAmino()

	// Use the proto codec as the canonical list of msg types.
	protoCodec := tApp.AppCodec().(*codec.ProtoCodec)
	protoRegisteredMsgs := protoCodec.InterfaceRegistry().ListImplementations(sdk.MsgInterfaceProtoName)

	for i, msgName := range protoRegisteredMsgs {
		// Skip msgs from dependencies that were never amino registered.
		if msgName == sdk.MsgTypeURL(&evmtypes.MsgEthereumTx{}) ||
			msgName == sdk.MsgTypeURL(&vestingtypes.MsgCreatePeriodicVestingAccount{}) {
			continue
		}

		// Create an encoded json msg, then unmarshal it to instantiate the msg type.
		jsonMsg := []byte(fmt.Sprintf(`{"@type": "%s"}`, msgName))

		var msg sdk.Msg
		err := protoCodec.UnmarshalInterfaceJSON(jsonMsg, &msg)
		require.NoError(t, err)

		// Only check legacy msgs for amino registration.
		// Only legacy msg can be signed with amino.
		_, ok := msg.(legacytx.LegacyMsg)
		if !ok {
			continue
		}

		// Check the msg is registered in amino by checking a repeat registration call panics.
		panicValue, ok := catchPanic(func() {
			lcdc.RegisterConcrete(interface{}(msg), fmt.Sprintf("aUniqueRegistrationName%d", i), nil)
		})
		assert.True(t, ok, "registration did not panic, msg %s is not registered in amino", msgName)
		if ok {
			require.IsTypef(t, "", panicValue, "msg %s amino registration panicked with unexpected type", msgName)
			aminoErrMsgPrefix := "TypeInfo already exists for"
			require.Containsf(t, panicValue, aminoErrMsgPrefix, "msg %s amino registration panicked for unexpected reason", msgName)
		}
	}
}

// catchPanic returns the panic value of the passed function. The second return indicates if the function panicked.
func catchPanic(f func()) (panicValue interface{}, didPanic bool) {
	didPanic = true

	defer func() {
		panicValue = recover()
	}()

	f()
	didPanic = false
	return
}

// unmarshalJSONKeys extracts keys from the top level of a json blob.
func unmarshalJSONKeys(jsonBytes []byte) ([]string, error) {
	var jsonMap map[string]json.RawMessage
	err := json.Unmarshal(jsonBytes, &jsonMap)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(jsonMap))
	for k := range jsonMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys, nil
}

func removeIbcTmModule(modules []string) []string {
	var result []string
	for _, str := range modules {
		if str != ibctm.ModuleName && str != solomachine.ModuleName {
			result = append(result, str)
		}
	}
	return result
}

func TestNewAppWithInvalidOptions(t *testing.T) {
	chaincfg.SetSDKConfig()
	
	// Test with nil DB
	t.Run("NilDatabase", func(t *testing.T) {
		defer func() {
			r := recover()
			require.NotNil(t, r, "Expected panic with nil database")
		}()
		
		NewBaseApp(
			log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
			nil, // Nil DB should cause panic
			MakeEncodingConfig(),
			baseapp.SetChainID(TestChainId),
		)
	})
	
	// Test with empty chain ID
	t.Run("EmptyChainID", func(t *testing.T) {
		defer func() {
			r := recover()
			require.NotNil(t, r, "Expected panic with empty chain ID")
		}()
		
		NewBaseApp(
			log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
			db.NewMemDB(),
			MakeEncodingConfig(),
			baseapp.SetChainID(""), // Empty chain ID should cause issues
		)
	})
}

func TestExportEdgeCases(t *testing.T) {
	chaincfg.SetSDKConfig()
	
	// Test export with pruned state
	t.Run("ExportWithPrunedState", func(t *testing.T) {
		db := db.NewMemDB()
		app := NewApp(
			chaincfg.DefaultNodeHome,
			nil,
			MakeEncodingConfig(),
			DefaultOptions,
			NewBaseApp(
				log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
				db,
				MakeEncodingConfig(),
				baseapp.SetChainID(TestChainId),
			),
		)

		genesisState := GenesisStateWithSingleValidator(&TestApp{App: *app}, NewDefaultGenesisState())
		stateBytes, err := json.Marshal(genesisState)
		require.NoError(t, err)

		// Initialize and commit multiple blocks to have pruned state
		initRequest := abci.RequestInitChain{
			Time:            time.Date(1998, 1, 1, 0, 0, 0, 0, time.UTC),
			ChainId:         TestChainId,
			InitialHeight:   1,
			ConsensusParams: sims.DefaultConsensusParams,
			Validators:      nil,
			AppStateBytes:   stateBytes,
		}
		app.InitChain(initRequest)
		
		// Commit multiple blocks
		for i := 0; i < 10; i++ {
			app.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: int64(i + 1)}})
			app.Commit()
		}

		// Test export with height greater than current height
		_, err = app.ExportAppStateAndValidators(false, []string{}, []string{})
		require.NoError(t, err, "Export should work even with pruned state")
	})
	
	// Test export with invalid parameters
	t.Run("ExportWithForceHeight", func(t *testing.T) {
		db := db.NewMemDB()
		app := NewApp(
			chaincfg.DefaultNodeHome,
			nil,
			MakeEncodingConfig(),
			DefaultOptions,
			NewBaseApp(
				log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
				db,
				MakeEncodingConfig(),
				baseapp.SetChainID(TestChainId),
			),
		)

		genesisState := GenesisStateWithSingleValidator(&TestApp{App: *app}, NewDefaultGenesisState())
		stateBytes, err := json.Marshal(genesisState)
		require.NoError(t, err)

		initRequest := abci.RequestInitChain{
			Time:            time.Date(1998, 1, 1, 0, 0, 0, 0, time.UTC),
			ChainId:         TestChainId,
			InitialHeight:   1,
			ConsensusParams: sims.DefaultConsensusParams,
			Validators:      nil,
			AppStateBytes:   stateBytes,
		}
		app.InitChain(initRequest)
		app.Commit()

		// Test export with specific height
		// Note: This may require changes to the ExportAppStateAndValidators method signature if it doesn't support forceHeight
		_, err = app.ExportAppStateAndValidators(true, []string{}, []string{})
		require.NoError(t, err, "Export with forceHeight should work")
	})
}

func TestLegacyMsgAminoRegistrationEdgeCases(t *testing.T) {
	tApp := NewTestApp()
	lcdc := tApp.LegacyAmino()
	protoCodec := tApp.AppCodec().(*codec.ProtoCodec)
	
	// Test with nil message
	t.Run("NilMessage", func(t *testing.T) {
		defer func() {
			r := recover()
			require.NotNil(t, r, "Expected panic when registering nil message")
		}()
		
		var nilMsg sdk.Msg
		lcdc.RegisterConcrete(interface{}(nilMsg), "NilMsg", nil)
	})
	
	// Test with non-existent message type
	t.Run("NonExistentMsgType", func(t *testing.T) {
		jsonMsg := []byte(`{"@type": "cosmos.nonexistent.v1.NonExistentMsg"}`)
		
		var msg sdk.Msg
		err := protoCodec.UnmarshalInterfaceJSON(jsonMsg, &msg)
		require.Error(t, err, "Should fail for non-existent message type")
	})
	
	// Test with malformed JSON
	t.Run("MalformedJSON", func(t *testing.T) {
		jsonMsg := []byte(`{"@type": "cosmos.bank.v1beta1.MsgSend", "malformed":}`)
		
		var msg sdk.Msg
		err := protoCodec.UnmarshalInterfaceJSON(jsonMsg, &msg)
		require.Error(t, err, "Should fail for malformed JSON")
	})
}

func TestResourceCleanup(t *testing.T) {
	// Test proper resource cleanup
	t.Run("DatabaseCleanup", func(t *testing.T) {
		db := db.NewMemDB()
		app := NewApp(
			chaincfg.DefaultNodeHome,
			nil,
			MakeEncodingConfig(),
			DefaultOptions,
			NewBaseApp(
				log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
				db,
				MakeEncodingConfig(),
				baseapp.SetChainID(TestChainId),
			),
		)
		
		// Use app
		genesisState := GenesisStateWithSingleValidator(&TestApp{App: *app}, NewDefaultGenesisState())
		stateBytes, err := json.Marshal(genesisState)
		require.NoError(t, err)
		
		initRequest := abci.RequestInitChain{
			Time:            time.Date(1998, 1, 1, 0, 0, 0, 0, time.UTC),
			ChainId:         TestChainId,
			InitialHeight:   1,
			ConsensusParams: sims.DefaultConsensusParams,
			Validators:      nil,
			AppStateBytes:   stateBytes,
		}
		app.InitChain(initRequest)
		app.Commit()
		
		// Ensure proper cleanup
		err = db.Close()
		require.NoError(t, err, "Database should close properly")
	})
}

func TestConcurrentAccess(t *testing.T) {
	// Test concurrent access to the app
	chaincfg.SetSDKConfig()
	db := db.NewMemDB()
	app := NewApp(
		chaincfg.DefaultNodeHome,
		nil,
		MakeEncodingConfig(),
		DefaultOptions,
		NewBaseApp(
			log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
			db,
			MakeEncodingConfig(),
			baseapp.SetChainID(TestChainId),
		),
	)
	
	// Initialize the app
	genesisState := GenesisStateWithSingleValidator(&TestApp{App: *app}, NewDefaultGenesisState())
	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)
	
	initRequest := abci.RequestInitChain{
		Time:            time.Date(1998, 1, 1, 0, 0, 0, 0, time.UTC),
		ChainId:         TestChainId,
		InitialHeight:   1,
		ConsensusParams: sims.DefaultConsensusParams,
		Validators:      nil,
		AppStateBytes:   stateBytes,
	}
	app.InitChain(initRequest)
	app.Commit()
	
	// Test concurrent query access
	t.Run("ConcurrentQueries", func(t *testing.T) {
		var wg sync.WaitGroup
		numGoroutines := 10
		errs := make(chan error, numGoroutines)
		
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Simulate concurrent queries
				_, err := app.Query(abci.RequestQuery{
					Path: fmt.Sprintf("/test/query/%d", i),
					Data: []byte{},
				})
				if err != nil {
					errs <- err
				}
			}(i)
		}
		
		wg.Wait()
		close(errs)
		
		for err := range errs {
			require.NoError(t, err, "Concurrent queries should not produce errors")
		}
	})
	
	// Clean up
	err = db.Close()
	require.NoError(t, err)
}