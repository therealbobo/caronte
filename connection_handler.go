package main

import (
	"encoding/binary"
	"github.com/flier/gohs/hyperscan"
	"github.com/google/gopacket"
	"github.com/google/gopacket/tcpassembly"
	log "github.com/sirupsen/logrus"
	"hash/fnv"
	"net"
	"sync"
	"time"
)

const initialConnectionsCapacity = 1024
const initialScannersCapacity = 1024

type BiDirectionalStreamFactory struct {
	storage        Storage
	serverNet      net.IPNet
	connections    map[StreamFlow]ConnectionHandler
	mConnections   sync.Mutex
	rulesManager   RulesManager
	rulesDatabase  RulesDatabase
	mRulesDatabase sync.Mutex
	scanners       []Scanner
}

type StreamFlow [4]gopacket.Endpoint

type Scanner struct {
	scratch *hyperscan.Scratch
	version RowID
}

type ConnectionHandler interface {
	Complete(handler *StreamHandler)
	Storage() Storage
	PatternsDatabase() hyperscan.StreamDatabase
	PatternsDatabaseSize() int
}

type connectionHandlerImpl struct {
	factory        *BiDirectionalStreamFactory
	connectionFlow StreamFlow
	mComplete      sync.Mutex
	otherStream    *StreamHandler
}

func NewBiDirectionalStreamFactory(storage Storage, serverNet net.IPNet,
	rulesManager RulesManager) *BiDirectionalStreamFactory {

	factory := &BiDirectionalStreamFactory{
		storage:        storage,
		serverNet:      serverNet,
		connections:    make(map[StreamFlow]ConnectionHandler, initialConnectionsCapacity),
		mConnections:   sync.Mutex{},
		rulesManager:   rulesManager,
		mRulesDatabase: sync.Mutex{},
		scanners:       make([]Scanner, 0, initialScannersCapacity),
	}

	go factory.updateRulesDatabaseService()
	return factory
}

func (factory *BiDirectionalStreamFactory) updateRulesDatabaseService() {
	for {
		select {
		case rulesDatabase, ok := <-factory.rulesManager.DatabaseUpdateChannel():
			if !ok {
				return
			}
			factory.mRulesDatabase.Lock()
			scanners := factory.scanners
			factory.scanners = factory.scanners[:0]

			for _, s := range scanners {
				err := s.scratch.Realloc(rulesDatabase.database)
				if err != nil {
					log.WithError(err).Error("failed to realloc an existing scanner")
				} else {
					s.version = rulesDatabase.version
					factory.scanners = append(factory.scanners, s)
				}
			}

			factory.rulesDatabase = rulesDatabase
			factory.mRulesDatabase.Unlock()
		}
	}
}

func (factory *BiDirectionalStreamFactory) takeScanner() Scanner {
	factory.mRulesDatabase.Lock()
	defer factory.mRulesDatabase.Unlock()

	if len(factory.scanners) == 0 {
		scratch, err := hyperscan.NewScratch(factory.rulesDatabase.database)
		if err != nil {
			log.WithError(err).Fatal("failed to alloc a new scratch")
		}

		return Scanner{
			scratch: scratch,
			version: factory.rulesDatabase.version,
		}
	}

	index := len(factory.scanners) - 1
	scanner := factory.scanners[index]
	factory.scanners = factory.scanners[:index]

	return scanner
}

func (factory *BiDirectionalStreamFactory) releaseScanner(scanner Scanner) {
	factory.mRulesDatabase.Lock()
	defer factory.mRulesDatabase.Unlock()

	if scanner.version != factory.rulesDatabase.version {
		err := scanner.scratch.Realloc(factory.rulesDatabase.database)
		if err != nil {
			log.WithError(err).Error("failed to realloc an existing scanner")
			return
		}
		scanner.version = factory.rulesDatabase.version
	}
	factory.scanners = append(factory.scanners, scanner)
}

func (factory *BiDirectionalStreamFactory) New(netFlow, transportFlow gopacket.Flow) tcpassembly.Stream {
	flow := StreamFlow{netFlow.Src(), netFlow.Dst(), transportFlow.Src(), transportFlow.Dst()}
	invertedFlow := StreamFlow{netFlow.Dst(), netFlow.Src(), transportFlow.Dst(), transportFlow.Src()}

	factory.mConnections.Lock()
	connection, isPresent := factory.connections[invertedFlow]
	isServer := factory.serverNet.Contains(netFlow.Src().Raw())
	if isPresent {
		delete(factory.connections, invertedFlow)
	} else {
		var connectionFlow StreamFlow
		if isServer {
			connectionFlow = invertedFlow
		} else {
			connectionFlow = flow
		}
		connection = &connectionHandlerImpl{
			connectionFlow: connectionFlow,
			mComplete:      sync.Mutex{},
			factory:        factory,
		}
		factory.connections[flow] = connection
	}
	factory.mConnections.Unlock()

	streamHandler := NewStreamHandler(connection, flow, factory.takeScanner(), !isServer)

	return &streamHandler
}

func (ch *connectionHandlerImpl) Complete(handler *StreamHandler) {
	ch.factory.releaseScanner(handler.scanner)
	ch.mComplete.Lock()
	if ch.otherStream == nil {
		ch.otherStream = handler
		ch.mComplete.Unlock()
		return
	}
	ch.mComplete.Unlock()

	var startedAt, closedAt time.Time
	if handler.firstPacketSeen.Before(ch.otherStream.firstPacketSeen) {
		startedAt = handler.firstPacketSeen
	} else {
		startedAt = ch.otherStream.firstPacketSeen
	}

	if handler.lastPacketSeen.After(ch.otherStream.lastPacketSeen) {
		closedAt = handler.lastPacketSeen
	} else {
		closedAt = ch.otherStream.lastPacketSeen
	}

	var client, server *StreamHandler
	if handler.streamFlow == ch.connectionFlow {
		client = handler
		server = ch.otherStream
	} else {
		client = ch.otherStream
		server = handler
	}

	connectionID := CustomRowID(ch.connectionFlow.Hash(), startedAt)
	connection := Connection{
		ID:              connectionID,
		SourceIP:        ch.connectionFlow[0].String(),
		DestinationIP:   ch.connectionFlow[1].String(),
		SourcePort:      binary.BigEndian.Uint16(ch.connectionFlow[2].Raw()),
		DestinationPort: binary.BigEndian.Uint16(ch.connectionFlow[3].Raw()),
		StartedAt:       startedAt,
		ClosedAt:        closedAt,
		ClientBytes:     client.streamLength,
		ServerBytes:     server.streamLength,
		ClientDocuments: len(client.documentsIDs),
		ServerDocuments: len(server.documentsIDs),
		ProcessedAt:     time.Now(),
	}
	ch.factory.rulesManager.FillWithMatchedRules(&connection, client.patternMatches, server.patternMatches)

	_, err := ch.Storage().Insert(Connections).One(connection)
	if err != nil {
		log.WithError(err).WithField("connection", connection).Error("failed to insert a connection")
		return
	}

	streamsIDs := append(client.documentsIDs, server.documentsIDs...)
	if len(streamsIDs) > 0 {
		n, err := ch.Storage().Update(ConnectionStreams).
			Filter(OrderedDocument{{"_id", UnorderedDocument{"$in": streamsIDs}}}).
			Many(UnorderedDocument{"connection_id": connectionID})
		if err != nil {
			log.WithError(err).WithField("connection", connection).Error("failed to update connection streams")
		} else if int(n) != len(streamsIDs) {
			log.WithError(err).WithField("connection", connection).Error("failed to update all connections streams")
		}
	}
}

func (ch *connectionHandlerImpl) Storage() Storage {
	return ch.factory.storage
}

func (ch *connectionHandlerImpl) PatternsDatabase() hyperscan.StreamDatabase {
	return ch.factory.rulesDatabase.database
}

func (ch *connectionHandlerImpl) PatternsDatabaseSize() int {
	return ch.factory.rulesDatabase.databaseSize
}

func (sf StreamFlow) Hash() uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write(sf[0].Raw())
	_, _ = hash.Write(sf[1].Raw())
	_, _ = hash.Write(sf[2].Raw())
	_, _ = hash.Write(sf[3].Raw())
	return hash.Sum64()
}
